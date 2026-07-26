<script setup lang="ts">
import { computed } from 'vue'
import { renderMarkdown } from '../teaching/renderMarkdown'

const props = defineProps<{ source: string }>()
const rendered = computed(() => {
  try {
    return renderMarkdown(props.source)
  } catch {
    return '<p>回答内容过长，暂无法显示。</p>'
  }
})
</script>

<template>
  <div class="final-answer" v-html="rendered"></div>
</template>

<style scoped>
.final-answer{line-height:1.75;overflow-wrap:anywhere}.final-answer :deep(pre){overflow:auto;padding:12px;border-radius:8px;background:#f4f7fb}.final-answer :deep(.math-block){overflow:auto;padding-block:4px}
</style>
