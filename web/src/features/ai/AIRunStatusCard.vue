<script setup lang="ts">
import { computed } from 'vue'
import type { AIRun } from './types'

const props = withDefaults(defineProps<{ run: AIRun; requestId?: string; subscriptionErrorCode?: string; pending?: boolean }>(), {
  requestId: '',
  subscriptionErrorCode: '',
  pending: false,
})
defineEmits<{ cancel: []; retry: []; reconnect: [] }>()

const labels = {
  queued: '等待生成',
  streaming: '正在生成',
  succeeded: '回答完成',
  failed: '生成失败',
  cancelled: '已停止',
} as const
const action = computed(() => {
  if (props.subscriptionErrorCode) {
    return props.subscriptionErrorCode === 'forbidden' || props.subscriptionErrorCode === 'unauthorized'
      ? '登录状态已失效，请重新登录。'
      : '回答连接已中断，请重新连接。'
  }
  switch (props.run.errorCode) {
    case 'QUOTA_EXCEEDED': return '今日或本月额度已用完，请稍后再试。'
    case 'CONTEXT_TOO_LARGE': return '问题内容过长，请精简后重新提问。'
    case 'AI_DISABLED': return 'AI 答疑暂未开放，请联系老师。'
    case 'ATTACHMENT_NOT_READY': return '附件仍在处理中，请稍后重试。'
    default: return props.run.status === 'failed' ? '服务暂时不可用，请重试。' : ''
  }
})
</script>

<template>
  <section class="status-card" :aria-label="`生成状态：${subscriptionErrorCode ? '连接中断' : labels[run.status]}`">
    <div><strong>{{ subscriptionErrorCode ? '连接中断' : labels[run.status] }}</strong><small>第 {{ run.attemptNo }} 次尝试</small></div>
    <p v-if="action">{{ action }}</p>
    <p v-if="run.usage">本次用量：输入 {{ run.usage.inputTokens }}，输出 {{ run.usage.outputTokens }} tokens</p>
    <p v-if="requestId" class="support">支持编号：{{ requestId }}</p>
    <button v-if="subscriptionErrorCode" type="button" aria-label="重新连接回答" :disabled="pending" @click="$emit('reconnect')">重新连接</button>
    <button v-else-if="run.status==='queued'||run.status==='streaming'" type="button" aria-label="停止生成" :disabled="pending" @click="$emit('cancel')">停止生成</button>
    <button v-else-if="run.status==='failed'||run.status==='cancelled'" type="button" aria-label="重试生成" :disabled="pending" @click="$emit('retry')">重新生成</button>
  </section>
</template>

<style scoped>
.status-card{display:grid;gap:9px;padding:15px;border:1px solid #c9d9e8;border-radius:10px;background:#f8fbfe}.status-card>div{display:flex;align-items:center;gap:12px}.status-card small,.support{color:#617187}.status-card p{margin:0}.status-card button{justify-self:start;padding:8px 13px;border:1px solid #b7c9da;border-radius:7px;background:#fff;color:#244563;font:inherit}
</style>
