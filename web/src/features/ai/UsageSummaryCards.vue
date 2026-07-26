<script setup lang="ts">
import type { UsageSummary } from './adminApi'
import { formatMicroUSD } from './UsageRunTable.vue'

defineProps<{ summary: UsageSummary }>()
</script>

<template>
  <section class="summary-grid" aria-label="用量概览">
    <article><span>请求数</span><strong>{{ summary.requests }}</strong></article>
    <article><span>成功</span><strong>{{ summary.succeeded }}</strong></article>
    <article><span>失败或取消</span><strong>{{ summary.failed }}</strong></article>
    <article><span>输入 Token</span><strong>{{ summary.inputTokens }}</strong></article>
    <article><span>输出 Token</span><strong>{{ summary.outputTokens }}</strong></article>
    <article><span>费用（USD）</span><strong>{{ formatMicroUSD(summary.costMicroUSD) }}</strong></article>
    <article><span>未知用量</span><strong>{{ summary.unknownUsage }}</strong></article>
    <article><span>平均首字延迟</span><strong>{{ summary.averageFirstByteMs }} ms</strong></article>
    <article><span>平均总耗时</span><strong>{{ summary.averageTotalMs }} ms</strong></article>
  </section>
</template>

<style scoped>
.summary-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(155px,1fr));gap:12px;margin:20px 0 28px}.summary-grid article{display:grid;gap:8px;padding:16px;border:1px solid #dbe4f0;border-radius:12px;background:#fff}.summary-grid span{color:#63758a;font-size:.84rem}.summary-grid strong{overflow-wrap:anywhere;color:#183b5d;font-size:1.22rem}
</style>
