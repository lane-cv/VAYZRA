<script setup lang="ts">
import { computed } from 'vue'
import { renderMarkdown } from './renderMarkdown'

const props = defineProps<{ source: string }>()
const rendered = computed(() => {
  try { return renderMarkdown(props.source) }
  catch { return '<p>正文过长，暂时无法预览。</p>' }
})
</script>

<template><article class="markdown-preview" aria-label="课程正文预览" v-html="rendered"></article></template>

<style scoped>
.markdown-preview{min-height:240px;padding:20px;border:1px solid #d7e2ed;border-radius:10px;background:#fff;line-height:1.75;overflow-wrap:anywhere}.markdown-preview:deep(img){max-width:100%}.markdown-preview:deep(pre){overflow:auto;padding:12px;border-radius:8px;background:#eef3f8}
</style>
