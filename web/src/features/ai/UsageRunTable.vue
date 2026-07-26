<script lang="ts">
const MICRO_USD = /^-?\d+$/

export function formatMicroUSD(raw: string): string {
  if (!MICRO_USD.test(raw)) return '未知'
  const amount = BigInt(raw)
  const negative = amount < 0n
  const absolute = negative ? -amount : amount
  const whole = (absolute / 1_000_000n).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const fraction = (absolute % 1_000_000n).toString().padStart(6, '0')
  return `${negative ? '-' : ''}$${whole}.${fraction}`
}
</script>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { UsageRun } from './adminApi'

defineProps<{ items: UsageRun[] }>()

const mediaQuery = typeof window === 'undefined' || typeof window.matchMedia !== 'function'
  ? undefined
  : window.matchMedia('(max-width: 899px)')
const compact = ref(mediaQuery?.matches ?? false)
function updateLayout(event: MediaQueryListEvent) {
  compact.value = event.matches
}
onMounted(() => mediaQuery?.addEventListener('change', updateLayout))
onBeforeUnmount(() => mediaQuery?.removeEventListener('change', updateLayout))

const statusLabels: Record<UsageRun['status'], string> = {
  queued: '排队中',
  streaming: '生成中',
  succeeded: '成功',
  failed: '失败',
  cancelled: '已取消',
}
const sourceLabels: Record<UsageRun['usageSource'], string> = {
  upstream: '供应商',
  estimated: '估算',
  unknown: '未知',
}

function statusLabel(status: UsageRun['status']): string {
  return statusLabels[status]
}
function sourceLabel(source: UsageRun['usageSource']): string {
  return sourceLabels[source]
}
function errorLabel(category?: string): string {
  if (!category) return '—'
  if (category === 'quota_estimation_anomaly') return '额度估算异常'
  return category
}
function timeLabel(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(new Date(value))
}
</script>

<template>
  <div v-if="!compact" class="desktop-table">
    <table aria-label="AI 用量运行记录">
      <thead>
        <tr>
          <th>时间</th><th>学生</th><th>模型</th><th>状态</th><th>Token</th>
          <th>来源</th><th>费用（USD）</th><th>耗时</th><th>错误类别</th><th>运行 ID</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="item in items" :key="item.id">
          <td>{{ timeLabel(item.createdAt) }}</td>
          <td>{{ item.studentDisplayName }}（{{ item.studentUsername }}）</td>
          <td>{{ item.modelLabel }}</td>
          <td><span class="status" :data-status="item.status">{{ statusLabel(item.status) }}</span></td>
          <td>输入 {{ item.inputTokens }} / 输出 {{ item.outputTokens }}</td>
          <td>{{ sourceLabel(item.usageSource) }}</td>
          <td>{{ formatMicroUSD(item.costMicroUSD) }}</td>
          <td>首字 {{ item.firstByteMs ?? '—' }} ms / 总计 {{ item.totalMs ?? '—' }} ms</td>
          <td>{{ errorLabel(item.errorCategory) }}</td>
          <td><code>{{ item.id }}</code></td>
        </tr>
      </tbody>
    </table>
  </div>
  <div v-else class="mobile-cards" aria-label="AI 用量运行记录（移动版）">
    <article v-for="item in items" :key="item.id" class="mobile-run-card">
      <h3>{{ statusLabel(item.status) }} · {{ timeLabel(item.createdAt) }}</h3>
      <dl>
        <div><dt>学生：</dt><dd>{{ item.studentDisplayName }}（{{ item.studentUsername }}）</dd></div>
        <div><dt>模型：</dt><dd>{{ item.modelLabel }}</dd></div>
        <div><dt>Token：</dt><dd>输入 {{ item.inputTokens }} / 输出 {{ item.outputTokens }}</dd></div>
        <div><dt>来源：</dt><dd>{{ sourceLabel(item.usageSource) }}</dd></div>
        <div><dt>费用（USD）：</dt><dd>{{ formatMicroUSD(item.costMicroUSD) }}</dd></div>
        <div><dt>耗时：</dt><dd>首字 {{ item.firstByteMs ?? '—' }} ms / 总计 {{ item.totalMs ?? '—' }} ms</dd></div>
        <div><dt>错误类别：</dt><dd>{{ errorLabel(item.errorCategory) }}</dd></div>
        <div><dt>运行 ID：</dt><dd><code>{{ item.id }}</code></dd></div>
      </dl>
    </article>
  </div>
</template>

<style scoped>
.desktop-table{overflow-x:auto;border:1px solid #dbe4f0;border-radius:12px;background:#fff}table{width:100%;border-collapse:collapse;min-width:1050px}th,td{padding:12px;text-align:left;vertical-align:top;border-bottom:1px solid #e7edf4;font-size:.86rem}th{color:#50657a;background:#f7f9fc;white-space:nowrap}tbody tr:last-child td{border-bottom:0}code{overflow-wrap:anywhere;font-size:.78rem}.status{font-weight:700}.status[data-status=succeeded]{color:#237344}.status[data-status=failed],.status[data-status=cancelled]{color:#a3473d}.mobile-cards{display:grid;gap:12px}.mobile-run-card{padding:16px;border:1px solid #dbe4f0;border-radius:12px;background:#fff}.mobile-run-card h3{margin:0 0 12px;font-size:1rem}.mobile-run-card dl{display:grid;gap:8px;margin:0}.mobile-run-card dl div{display:grid;grid-template-columns:7rem 1fr;gap:8px}.mobile-run-card dt{color:#63758a}.mobile-run-card dd{min-width:0;margin:0;overflow-wrap:anywhere}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto!important;transition:none!important;animation:none!important}}
</style>
