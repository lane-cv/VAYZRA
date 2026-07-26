<script lang="ts">
export function dollarsPerMillionToMicroUSD(value: string): number | null {
  const normalized = value.trim()
  const match = /^(\d+)(?:\.(\d{1,6}))?$/.exec(normalized)
  if (!match) return null
  const whole = BigInt(match[1])
  const fraction = BigInt((match[2] ?? '').padEnd(6, '0'))
  const amount = whole * 1_000_000n + fraction
  return amount <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(amount) : null
}

export function microUSDToDollarsPerMillion(value: number): string {
  if (!Number.isSafeInteger(value) || value < 0) return ''
  const whole = Math.floor(value / 1_000_000)
  const fraction = String(value % 1_000_000).padStart(6, '0').replace(/0+$/, '')
  return fraction ? `${whole}.${fraction}` : String(whole)
}
</script>

<script setup lang="ts">
import { onBeforeMount, reactive, ref } from 'vue'
import { APIError } from '../../api/client'
import {
  listModels,
  listProviders,
  putModel,
  type ModelView,
  type ProviderView,
} from './adminApi'

type Modality = 'text' | 'vision'
type RouteForm = {
  id: string
  upstreamModelId: string
  contextTokens: number
  maxOutputTokens: number
  imageQuotaTokens: number
  inputPrice: string
  outputPrice: string
  connectTimeoutMs: number
  responseHeaderTimeoutMs: number
  idleStreamTimeoutMs: number
  totalTimeoutMs: number
  enabled: boolean
  clearQuotaBlock: boolean
  version: number
}

const providers = ref<ProviderView[]>([])
const providerId = ref('')
const loading = ref(false)
const pending = ref<Modality>()
const error = ref('')
const requestId = ref('')
const notice = ref('')

function emptyForm(): RouteForm {
  return {
    id: '',
    upstreamModelId: '',
    contextTokens: 128000,
    maxOutputTokens: 4096,
    imageQuotaTokens: 2048,
    inputPrice: '0',
    outputPrice: '0',
    connectTimeoutMs: 5000,
    responseHeaderTimeoutMs: 30000,
    idleStreamTimeoutMs: 30000,
    totalTimeoutMs: 120000,
    enabled: true,
    clearQuotaBlock: false,
    version: 0,
  }
}

const forms = reactive<Record<Modality, RouteForm>>({
  text: emptyForm(),
  vision: emptyForm(),
})

function setForm(modality: Modality, model?: ModelView) {
  const next = model ? {
    id: model.id,
    upstreamModelId: model.upstreamModelId,
    contextTokens: model.contextTokens,
    maxOutputTokens: model.maxOutputTokens,
    imageQuotaTokens: model.imageQuotaTokens,
    inputPrice: microUSDToDollarsPerMillion(model.inputPriceMicroUsd),
    outputPrice: microUSDToDollarsPerMillion(model.outputPriceMicroUsd),
    connectTimeoutMs: model.connectTimeoutMs,
    responseHeaderTimeoutMs: model.responseHeaderTimeoutMs,
    idleStreamTimeoutMs: model.idleStreamTimeoutMs,
    totalTimeoutMs: model.totalTimeoutMs,
    enabled: model.enabled,
    clearQuotaBlock: false,
    version: model.version,
  } : emptyForm()
  Object.assign(forms[modality], next)
}

function failure(reason: unknown, fallback: string) {
  return reason instanceof APIError
    ? { message: reason.message || fallback, requestId: reason.requestId, conflict: reason.status === 409 || reason.code === 'config_conflict' }
    : { message: fallback, requestId: '', conflict: false }
}

async function loadModels() {
  error.value = ''
  requestId.value = ''
  if (!providerId.value) {
    setForm('text')
    setForm('vision')
    return
  }
  loading.value = true
  try {
    const models = await listModels(providerId.value)
    setForm('text', models.find((model) => model.modality === 'text'))
    setForm('vision', models.find((model) => model.modality === 'vision'))
  } catch (reason) {
    const details = failure(reason, '模型路由加载失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
  } finally {
    loading.value = false
  }
}

async function initialize() {
  loading.value = true
  try {
    providers.value = await listProviders()
    providerId.value = providers.value.find((provider) => provider.active)?.id ?? providers.value[0]?.id ?? ''
  } catch (reason) {
    const details = failure(reason, '供应商加载失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
    loading.value = false
    return
  }
  loading.value = false
  await loadModels()
}

function timeoutError(form: RouteForm): string {
  if (form.connectTimeoutMs < 100 || form.connectTimeoutMs > 30000) return '连接超时须为 100–30000 ms'
  if (form.responseHeaderTimeoutMs < 1000 || form.responseHeaderTimeoutMs > 120000) return '响应头超时须为 1000–120000 ms'
  if (form.idleStreamTimeoutMs < 1000 || form.idleStreamTimeoutMs > 120000) return '流空闲超时须为 1000–120000 ms'
  if (form.totalTimeoutMs < form.responseHeaderTimeoutMs || form.totalTimeoutMs < form.idleStreamTimeoutMs || form.totalTimeoutMs > 600000) return '总超时不得低于响应头/流空闲超时，且不得超过 600000 ms'
  return ''
}

async function save(modality: Modality) {
  if (!providerId.value || pending.value) return
  error.value = ''
  requestId.value = ''
  notice.value = ''
  const form = forms[modality]
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(form.id)) {
    error.value = '请填写规范的模型 UUID'
    return
  }
  if (!form.upstreamModelId.trim() || form.contextTokens < 1 || form.maxOutputTokens < 1 || form.imageQuotaTokens < 1) {
    error.value = '模型标识和 Token 上限必须为正数'
    return
  }
  if (form.maxOutputTokens > form.contextTokens) {
    error.value = '最大输出 Token 不能超过上下文 Token'
    return
  }
  const timeoutMessage = timeoutError(form)
  if (timeoutMessage) {
    error.value = timeoutMessage
    return
  }
  const inputPrice = dollarsPerMillionToMicroUSD(form.inputPrice)
  const outputPrice = dollarsPerMillionToMicroUSD(form.outputPrice)
  if (inputPrice === null || outputPrice === null) {
    error.value = '价格须为非负美元金额，最多保留 6 位小数'
    return
  }
  pending.value = modality
  try {
    const model = await putModel(providerId.value, form.id, {
      upstreamModelId: form.upstreamModelId.trim(),
      modality,
      contextTokens: form.contextTokens,
      maxOutputTokens: form.maxOutputTokens,
      imageQuotaTokens: form.imageQuotaTokens,
      inputPriceMicroUsd: inputPrice,
      outputPriceMicroUsd: outputPrice,
      connectTimeoutMs: form.connectTimeoutMs,
      responseHeaderTimeoutMs: form.responseHeaderTimeoutMs,
      idleStreamTimeoutMs: form.idleStreamTimeoutMs,
      totalTimeoutMs: form.totalTimeoutMs,
      enabled: form.enabled,
      clearQuotaBlock: form.clearQuotaBlock,
      expectedVersion: form.version,
    })
    setForm(modality, model)
    notice.value = `${modality === 'text' ? '文本' : '视觉'}模型路由已保存`
  } catch (reason) {
    const details = failure(reason, '模型路由保存失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
    if (details.conflict) await loadModels()
  } finally {
    pending.value = undefined
  }
}

onBeforeMount(() => { void initialize() })
</script>

<template>
  <section class="editor" aria-labelledby="routing-heading">
    <h2 id="routing-heading">模型路由</h2>
    <label class="provider-select">供应商
      <select v-model="providerId" aria-label="模型供应商" :disabled="loading || !!pending" @change="loadModels">
        <option v-for="provider in providers" :key="provider.id" :value="provider.id">{{ provider.name }}</option>
      </select>
    </label>
    <p v-if="loading" role="status">正在加载模型路由…</p>
    <div v-if="error" role="alert"><p>{{ error }}<span v-if="requestId"> 支持编号：{{ requestId }}</span></p><button type="button" aria-label="重新加载模型路由" :disabled="loading || !!pending" @click="loadModels">重新加载</button></div>
    <p v-if="notice" role="status">{{ notice }}</p>
    <div class="route-grid">
      <form v-for="modality in (['text', 'vision'] as const)" :key="modality" :data-modality="modality" @submit.prevent="save(modality)">
        <fieldset :disabled="loading || !!pending">
          <legend>{{ modality === 'text' ? '文本模型路由' : '视觉模型路由' }}</legend>
          <label>模型 UUID<input v-model.trim="forms[modality].id" :aria-label="`${modality === 'text' ? '文本' : '视觉'}模型 UUID`" /></label>
          <label>上游模型<input v-model="forms[modality].upstreamModelId" :aria-label="`${modality === 'text' ? '文本' : '视觉'}上游模型`" /></label>
          <label>上下文 Token<input v-model.number="forms[modality].contextTokens" type="number" min="1" :aria-label="`${modality === 'text' ? '文本' : '视觉'}上下文 Token`" /></label>
          <label>最大输出 Token<input v-model.number="forms[modality].maxOutputTokens" type="number" min="1" :aria-label="`${modality === 'text' ? '文本' : '视觉'}最大输出 Token`" /></label>
          <label>图片配额 Token<input v-model.number="forms[modality].imageQuotaTokens" type="number" min="1" :aria-label="`${modality === 'text' ? '文本' : '视觉'}图片配额 Token`" /></label>
          <label>输入价格（美元/百万 Token）<input v-model="forms[modality].inputPrice" inputmode="decimal" :aria-label="`${modality === 'text' ? '文本' : '视觉'}输入价格`" /></label>
          <label>输出价格（美元/百万 Token）<input v-model="forms[modality].outputPrice" inputmode="decimal" :aria-label="`${modality === 'text' ? '文本' : '视觉'}输出价格`" /></label>
          <label>连接超时 ms<input v-model.number="forms[modality].connectTimeoutMs" type="number" min="100" max="30000" :aria-label="`${modality === 'text' ? '文本' : '视觉'}连接超时`" /></label>
          <label>响应头超时 ms<input v-model.number="forms[modality].responseHeaderTimeoutMs" type="number" min="1000" max="120000" :aria-label="`${modality === 'text' ? '文本' : '视觉'}响应头超时`" /></label>
          <label>流空闲超时 ms<input v-model.number="forms[modality].idleStreamTimeoutMs" type="number" min="1000" max="120000" :aria-label="`${modality === 'text' ? '文本' : '视觉'}流空闲超时`" /></label>
          <label>总超时 ms<input v-model.number="forms[modality].totalTimeoutMs" type="number" min="1000" max="600000" :aria-label="`${modality === 'text' ? '文本' : '视觉'}总超时`" /></label>
          <label class="check"><input v-model="forms[modality].enabled" type="checkbox" />启用此路由</label>
          <label v-if="forms[modality].version > 0" class="check"><input v-model="forms[modality].clearQuotaBlock" type="checkbox" />确认调整容量后清除异常封锁</label>
          <button type="submit" :aria-label="`保存${modality === 'text' ? '文本' : '视觉'}模型路由`" @click="save(modality)">{{ pending === modality ? '正在保存…' : '保存路由' }}</button>
        </fieldset>
      </form>
    </div>
  </section>
</template>

<style scoped>
.editor{display:grid;gap:16px}.editor h2{margin:0}.provider-select{display:grid;gap:6px;max-width:360px;font-weight:650}.provider-select select,fieldset input{padding:9px;border:1px solid #b9c9da;border-radius:8px;font:inherit}.route-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}fieldset{display:grid;gap:12px;padding:18px;border:1px solid #dbe4f0;border-radius:12px;background:#fff}legend{padding:0 6px;font-weight:800}fieldset label:not(.check){display:grid;gap:5px;font-weight:650}.check{display:flex;gap:8px;align-items:center}button{justify-self:start;border:1px solid #166cbb;border-radius:8px;background:#166cbb;color:#fff;padding:9px 12px;font:inherit;font-weight:650}@media(max-width:800px){.route-grid{grid-template-columns:1fr}}
</style>
