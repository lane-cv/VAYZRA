<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { APIError } from '../../api/client'
import { listStudentQuestions } from './studentApi'
import type { QuestionStatus, QuestionThread } from './types'
const items = ref<QuestionThread[]>([]), status = ref<QuestionStatus | ''>(''), nextCursor = ref<string>(), loading = ref(true), error = ref(''), requestId = ref('')
let controller: AbortController | undefined, generation = 0
const labels: Record<QuestionStatus,string> = { pending:'待处理', in_progress:'处理中', waiting_student:'等待我回复', completed:'已完成' }
async function load(cursor?: string) {
  const current = ++generation; controller?.abort(); controller = new AbortController(); loading.value = true; error.value = ''; requestId.value = ''
  try { const page = await listStudentQuestions({ status: status.value || undefined, limit: 20 }, cursor, controller.signal); if (current !== generation) return; items.value = page.items; nextCursor.value = page.nextCursor }
  catch (cause) { if (controller.signal.aborted || current !== generation) return; error.value = cause instanceof Error ? cause.message : '加载失败'; requestId.value = cause instanceof APIError ? cause.requestId : typeof cause === 'object' && cause && 'requestId' in cause ? String(cause.requestId) : '' }
  finally { if (current === generation) loading.value = false }
}
function changeFilter() { void load() }
onMounted(() => void load()); onBeforeUnmount(() => controller?.abort())
</script>
<template>
  <section class="questions" aria-labelledby="questions-title">
    <header><div><p class="eyebrow">老师答疑</p><h1 id="questions-title">我的问题</h1></div><RouterLink class="primary" to="/student/questions/new">提出新问题</RouterLink></header>
    <label>按状态筛选<select v-model="status" @change="changeFilter"><option value="">全部</option><option v-for="(label,key) in labels" :key="key" :value="key">{{ label }}</option></select></label>
    <p v-if="loading" role="status">正在加载问答…</p>
    <div v-else-if="error" role="alert"><p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p><button type="button" aria-label="重试加载问答" @click="load()">重试</button></div>
    <p v-else-if="!items.length" class="empty">还没有符合条件的问题。</p>
    <ul v-else><li v-for="thread in items" :key="thread.id"><RouterLink :to="`/student/questions/${encodeURIComponent(thread.id)}`"><strong>{{ thread.title }}</strong><span>{{ labels[thread.status] }}</span><time :datetime="thread.lastMessageAt">最近更新 {{ new Date(thread.lastMessageAt).toLocaleString('zh-CN') }}</time></RouterLink></li></ul>
    <button v-if="!loading && nextCursor" type="button" aria-label="下一页问答" @click="load(nextCursor)">下一页</button>
  </section>
</template>
<style scoped>.questions{max-width:900px}.questions>header{display:flex;justify-content:space-between;gap:18px;align-items:center}.eyebrow{color:#1673b9;font-weight:700}.questions h1{margin:.35rem 0 1rem}.primary{padding:10px 15px;border-radius:8px;background:#176faf;color:#fff;text-decoration:none}.questions label{display:flex;gap:10px;align-items:center}.questions select{padding:8px}.questions ul{display:grid;gap:10px;padding:0;list-style:none}.questions li a{display:grid;grid-template-columns:1fr auto;gap:8px;padding:16px;border:1px solid #dbe4f0;border-radius:11px;background:#fff;color:#182842;text-decoration:none}.questions time{grid-column:1/-1;color:#617086;font-size:.85rem}.empty{padding:24px;border:1px dashed #bdccdb;border-radius:10px;text-align:center;color:#617086}[role=alert]{color:#a33731}@media(max-width:560px){.questions>header{align-items:flex-start;flex-direction:column}.questions li a{grid-template-columns:1fr}}</style>
