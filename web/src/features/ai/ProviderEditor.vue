<script setup lang="ts">
import { onBeforeMount, ref } from 'vue'
import { APIError } from '../../api/client'
import {
  activateProvider,
  createProvider,
  listProviders,
  testProvider,
  updateProvider,
  type ProtocolMode,
  type ProviderView,
} from './adminApi'

const providers = ref<ProviderView[]>([])
const loading = ref(false)
const pending = ref(false)
const error = ref('')
const requestId = ref('')
const notice = ref('')
const editingId = ref<string>()
const replaceKey = ref(false)
const name = ref('')
const baseUrl = ref('')
const protocolMode = ref<ProtocolMode>('responses')
const apiKey = ref('')
const conflictFeedbackActive = ref(false)

function details(reason: unknown, fallback: string) {
  return reason instanceof APIError
    ? { message: reason.message || fallback, requestId: reason.requestId, conflict: reason.status === 409 || reason.code === 'config_conflict' }
    : { message: fallback, requestId: '', conflict: false }
}

function clearFeedback() {
  error.value = ''
  requestId.value = ''
  notice.value = ''
  conflictFeedbackActive.value = false
}

async function load(preserveFailure = false) {
  loading.value = true
  if (!preserveFailure) clearFeedback()
  try {
    providers.value = await listProviders()
    rebaseOpenEdit()
    return true
  } catch (reason) {
    if (!preserveFailure) {
      const failure = details(reason, '供应商配置加载失败，请稍后重试')
      error.value = failure.message
      requestId.value = failure.requestId
    }
    return false
  } finally {
    loading.value = false
  }
}

function beginCreate() {
  editingId.value = undefined
  replaceKey.value = true
  name.value = ''
  baseUrl.value = ''
  protocolMode.value = 'responses'
  apiKey.value = ''
  clearFeedback()
}

function beginEdit(provider: ProviderView) {
  editingId.value = provider.id
  replaceKey.value = false
  name.value = provider.name
  baseUrl.value = provider.baseUrl
  protocolMode.value = provider.protocolMode
  apiKey.value = ''
  clearFeedback()
}

function rebaseEdit(provider: ProviderView) {
  editingId.value = provider.id
  replaceKey.value = false
  name.value = provider.name
  baseUrl.value = provider.baseUrl
  protocolMode.value = provider.protocolMode
  apiKey.value = ''
}

function rebaseOpenEdit() {
  if (!editingId.value) return
  const latest = providers.value.find((provider) => provider.id === editingId.value)
  if (latest) {
    rebaseEdit(latest)
    return
  }
  editingId.value = undefined
  replaceKey.value = true
  name.value = ''
  baseUrl.value = ''
  protocolMode.value = 'responses'
}

async function reloadAfterConflict() {
  await load(true)
}

function retryLoad() {
  void load(conflictFeedbackActive.value)
}

function providerURLProtocol(value: string): 'http:' | 'https:' | '' {
  try {
    const protocol = new URL(value).protocol
    return protocol === 'http:' || protocol === 'https:' ? protocol : ''
  } catch {
    return ''
  }
}

function replaceProvider(next: ProviderView) {
  const index = providers.value.findIndex((provider) => provider.id === next.id)
  if (index < 0) providers.value = [...providers.value, next]
  else providers.value = providers.value.map((provider) => provider.id === next.id ? next : provider)
}

async function save() {
  if (pending.value) return
  clearFeedback()
  const normalizedName = name.value.trim()
  const normalizedURL = baseUrl.value.trim()
  if (!normalizedName) {
    error.value = '请填写供应商名称'
    return
  }
  if (!providerURLProtocol(normalizedURL)) {
    error.value = '请输入有效的 HTTP(S) 地址'
    return
  }
  if (!editingId.value && !apiKey.value) {
    error.value = '新建供应商必须填写 API Key'
    return
  }
  if (editingId.value && replaceKey.value && !apiKey.value) {
    error.value = '请输入新的 API Key'
    return
  }
  pending.value = true
  const secret = apiKey.value
  try {
    const current = providers.value.find((provider) => provider.id === editingId.value)
    const updated = editingId.value && current
      ? await updateProvider(editingId.value, {
          name: normalizedName,
          baseUrl: normalizedURL,
          protocolMode: protocolMode.value,
          apiKey: replaceKey.value ? secret : undefined,
          expectedVersion: current.version,
        })
      : await createProvider({
          name: normalizedName,
          baseUrl: normalizedURL,
          protocolMode: protocolMode.value,
          apiKey: secret,
        })
    // The secret is destroyed before any provider/read-view state is changed.
    apiKey.value = ''
    replaceKey.value = false
    editingId.value = updated.id
    replaceProvider(updated)
    notice.value = '已安全保存'
  } catch (reason) {
    apiKey.value = ''
    const failure = details(reason, '供应商保存失败，请稍后重试')
    error.value = failure.message
    requestId.value = failure.requestId
    if (failure.conflict) {
      conflictFeedbackActive.value = true
      await reloadAfterConflict()
    }
  } finally {
    pending.value = false
  }
}

async function activate(provider: ProviderView) {
  if (pending.value || !confirm(`确认将“${provider.name}”设为当前供应商？`)) return
  pending.value = true
  clearFeedback()
  try {
    const updated = await activateProvider(provider.id, provider.version)
    providers.value = providers.value.map((item) => item.id === updated.id
      ? updated
      : { ...item, active: false })
    notice.value = '当前供应商已更新'
  } catch (reason) {
    const failure = details(reason, '启用供应商失败，请稍后重试')
    error.value = failure.message
    requestId.value = failure.requestId
    if (failure.conflict) {
      conflictFeedbackActive.value = true
      await reloadAfterConflict()
    }
  } finally {
    pending.value = false
  }
}

async function testConnection(provider: ProviderView) {
  if (pending.value || !confirm('连接测试可能产生少量费用，是否继续？')) return
  pending.value = true
  clearFeedback()
  try {
    const result = await testProvider(provider.id)
    notice.value = `连接成功，耗时 ${result.latencyMs} ms`
  } catch (reason) {
    const failure = details(reason, '连接测试失败，请检查供应商配置')
    error.value = failure.message
    requestId.value = failure.requestId
  } finally {
    pending.value = false
  }
}

onBeforeMount(() => { void load() })
</script>

<template>
  <section class="editor" aria-labelledby="provider-heading">
    <div class="section-heading">
      <div><h2 id="provider-heading">供应商配置</h2><p>密钥仅在新建或显式替换时提交，已保存密钥不会回显。</p></div>
      <button type="button" aria-label="新建供应商" :disabled="pending" @click="beginCreate">新建供应商</button>
    </div>
    <p v-if="loading" role="status">正在加载供应商…</p>
    <div v-else class="provider-grid">
      <article v-for="provider in providers" :key="provider.id" class="provider-card">
        <h3>{{ provider.name }} <span v-if="provider.active">当前使用</span></h3>
        <p>{{ provider.baseUrl }}</p>
        <p>{{ provider.protocolMode === 'responses' ? 'Responses' : 'Chat Completions' }}</p>
        <p>{{ provider.hasKey ? '已安全保存' : '未配置密钥' }} · 更新时间 {{ provider.keyUpdatedAt }}</p>
        <div class="actions">
          <button type="button" :aria-label="`编辑 ${provider.name}`" :disabled="pending" @click="beginEdit(provider)">编辑</button>
          <button v-if="!provider.active" type="button" :aria-label="`启用 ${provider.name}`" :disabled="pending" @click="activate(provider)">设为当前</button>
          <button type="button" :aria-label="`测试 ${provider.name} 的连接`" :disabled="pending" @click="testConnection(provider)">测试连接</button>
        </div>
      </article>
      <p v-if="providers.length === 0">尚未配置供应商。</p>
    </div>

    <form class="form-card" novalidate @submit.prevent="save">
      <h3>{{ editingId ? '编辑供应商' : '新建供应商' }}</h3>
      <label>名称<input v-model="name" aria-label="供应商名称" :disabled="pending" /></label>
      <label>服务地址<input v-model="baseUrl" aria-label="供应商地址" inputmode="url" :disabled="pending" /></label>
      <p v-if="providerURLProtocol(baseUrl.trim()) === 'http:'" class="transport-warning" role="note">
        HTTP 地址仅可用于受控开发环境；服务器仍会执行网络与传输安全策略。
      </p>
      <label>协议模式
        <select v-model="protocolMode" aria-label="协议模式" :disabled="pending">
          <option value="chat_completions">Chat Completions</option>
          <option value="responses">Responses</option>
        </select>
      </label>
      <button v-if="editingId && !replaceKey" type="button" aria-label="替换 API Key" :disabled="pending" @click="replaceKey = true">替换 API Key</button>
      <label v-if="!editingId || replaceKey">API Key
        <input v-model="apiKey" aria-label="API Key" type="password" autocomplete="new-password" :disabled="pending" />
      </label>
      <div v-if="error" role="alert"><p>{{ error }}<span v-if="requestId"> 支持编号：{{ requestId }}</span></p><button type="button" aria-label="重新加载供应商" :disabled="pending" @click="retryLoad">重新加载</button></div>
      <p v-if="notice" role="status">{{ notice }}</p>
      <button type="submit" :disabled="pending">{{ pending ? '正在保存…' : '保存供应商' }}</button>
    </form>
  </section>
</template>

<style scoped>
.editor{display:grid;gap:20px}.section-heading{display:flex;justify-content:space-between;gap:20px;align-items:start}.section-heading h2,.form-card h3{margin-top:0}.section-heading p,.provider-card p{color:var(--hl-text-muted)}.provider-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:14px}.provider-card,.form-card{padding:18px;border:1px solid var(--hl-border);border-radius:12px;background:var(--hl-surface-solid)}.provider-card h3 span{margin-left:8px;color:#167244;font-size:.78rem}.actions{display:flex;flex-wrap:wrap;gap:8px}.form-card{display:grid;gap:14px}.form-card label{display:grid;gap:6px;font-weight:650}.form-card input,.form-card select{padding:10px;border:1px solid var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit}.transport-warning{margin:0;color:#805b08}button{border:1px solid var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text);padding:9px 12px;font:inherit;font-weight:650;cursor:pointer}button[type=submit]{justify-self:start;border-color:var(--hl-primary);background:var(--hl-primary);color:#fff}button:disabled{opacity:.6;cursor:wait}@media(max-width:640px){.section-heading{display:grid}}
</style>
