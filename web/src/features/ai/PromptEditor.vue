<script setup lang="ts">
import { onBeforeMount, reactive, ref } from 'vue'
import { APIError } from '../../api/client'
import { listPrompts, putPrompt, type PromptView } from './adminApi'
import type { AISubject } from './types'

type PromptForm = { body: string; version: number }
const forms = reactive<Record<AISubject, PromptForm>>({
  math: { body: '', version: 0 },
  physics: { body: '', version: 0 },
})
const loading = ref(false)
const pending = ref<AISubject>()
const error = ref('')
const requestId = ref('')
const notice = ref('')

function apply(prompts: PromptView[]) {
  for (const subject of ['math', 'physics'] as const) {
    const active = prompts.find((prompt) => prompt.subject === subject && prompt.active)
      ?? prompts.find((prompt) => prompt.subject === subject)
    forms[subject].body = active?.body ?? ''
    forms[subject].version = active?.version ?? 0
  }
}

async function load(preserveFailure = false) {
  loading.value = true
  if (!preserveFailure) {
    error.value = ''
    requestId.value = ''
  }
  try {
    apply(await listPrompts())
  } catch (reason) {
    if (!preserveFailure) {
      error.value = reason instanceof APIError ? reason.message : '提示词加载失败，请稍后重试'
      requestId.value = reason instanceof APIError ? reason.requestId : ''
    }
  } finally {
    loading.value = false
  }
}

async function save(subject: AISubject) {
  if (pending.value) return
  error.value = ''
  requestId.value = ''
  notice.value = ''
  const body = forms[subject].body.trim()
  if (!body) {
    error.value = '提示词不能为空'
    return
  }
  pending.value = subject
  try {
    const updated = await putPrompt(subject, { body, expectedVersion: forms[subject].version })
    forms[subject].body = updated.body
    forms[subject].version = updated.version
    notice.value = `${subject === 'math' ? '数学' : '物理'}提示词已保存`
  } catch (reason) {
    error.value = reason instanceof APIError ? reason.message : '提示词保存失败，请稍后重试'
    requestId.value = reason instanceof APIError ? reason.requestId : ''
    if (reason instanceof APIError && (reason.status === 409 || reason.code === 'config_conflict')) {
      await load(true)
    }
  } finally {
    pending.value = undefined
  }
}

onBeforeMount(() => { void load() })
</script>

<template>
  <section class="editor" aria-labelledby="prompt-heading">
    <h2 id="prompt-heading">提示词</h2>
    <p>数学和物理提示词分别保存版本；冲突时会重新加载最新版本。</p>
    <p v-if="loading" role="status">正在加载提示词…</p>
    <div v-if="error" role="alert"><p>{{ error }}<span v-if="requestId"> 支持编号：{{ requestId }}</span></p><button type="button" aria-label="重新加载提示词" :disabled="loading || !!pending" @click="load()">重新加载</button></div>
    <p v-if="notice" role="status">{{ notice }}</p>
    <div class="prompt-grid">
      <form v-for="subject in (['math', 'physics'] as const)" :key="subject" @submit.prevent="save(subject)">
        <h3>{{ subject === 'math' ? '数学' : '物理' }} · 版本 {{ forms[subject].version }}</h3>
        <label :for="`${subject}-prompt`">{{ subject === 'math' ? '数学' : '物理' }}系统提示词</label>
        <textarea :id="`${subject}-prompt`" v-model="forms[subject].body" :aria-label="`${subject === 'math' ? '数学' : '物理'}提示词`" rows="14" maxlength="100000" :disabled="loading || !!pending"></textarea>
        <button type="submit" :aria-label="`保存${subject === 'math' ? '数学' : '物理'}提示词`" :disabled="loading || !!pending" @click="save(subject)">{{ pending === subject ? '正在保存…' : '保存提示词' }}</button>
      </form>
    </div>
  </section>
</template>

<style scoped>
.editor{display:grid;gap:12px}.editor h2{margin:0}.editor>p{color:var(--hl-text-muted)}.prompt-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.prompt-grid form{display:grid;gap:10px;padding:18px;border:1px solid var(--hl-border);border-radius:12px;background:var(--hl-surface-solid)}.prompt-grid h3{margin:0}.prompt-grid label{font-weight:650}.prompt-grid textarea{resize:vertical;padding:11px;border:1px solid var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit;line-height:1.6}.prompt-grid button{justify-self:start;border:1px solid var(--hl-primary);border-radius:8px;background:var(--hl-primary);color:#fff;padding:9px 12px;font:inherit;font-weight:650}@media(max-width:800px){.prompt-grid{grid-template-columns:1fr}}
</style>
