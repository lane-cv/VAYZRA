<script setup lang="ts">
import { onBeforeMount, onBeforeUnmount, ref, watch } from 'vue'
import { APIError } from '../../api/client'
import MarkdownPreview from '../teaching/MarkdownPreview.vue'
import ExternalVideoFrame from './ExternalVideoFrame.vue'
import { getStudentLesson, updateProgress } from './api'
import type { StudentLesson } from './types'

const props = defineProps<{ lessonId: string }>()
const lesson = ref<StudentLesson | null>(null)
const loading = ref(true)
const error = ref('')
let controller: AbortController | undefined
let progressTimer: ReturnType<typeof setTimeout> | undefined
let latestProgress: { revisionId: string; viewed: boolean; anchor: string; scrollRatio: number; observedAt: string } | undefined
let inFlightProgress: typeof latestProgress
let newestProgress = ''
let progressSending = false

async function load() {
  flushProgress()
  controller?.abort()
  controller = new AbortController()
  loading.value = true; error.value = ''; lesson.value = null
  try { lesson.value = await getStudentLesson(props.lessonId, controller.signal) }
  catch (cause) {
    if (!controller.signal.aborted) error.value = cause instanceof APIError && cause.status === 404 ? '课程不存在或暂不可访问' : cause instanceof APIError ? cause.message : '课程加载失败，请稍后重试'
  } finally { loading.value = false }
}
function recordScroll(event: Event) {
  if (!lesson.value) return
  const element = event.currentTarget as HTMLElement
  const distance = Math.max(1, element.scrollHeight - element.clientHeight)
  latestProgress = { revisionId: lesson.value.revisionId, viewed: true, anchor: currentAnchor(element), scrollRatio: Math.max(0, Math.min(1, element.scrollTop / distance)), observedAt: new Date().toISOString() }
  newestProgress = `${latestProgress.revisionId}:${latestProgress.observedAt}`
  scheduleProgress()
}
function currentAnchor(element: HTMLElement) {
  const headings = [...element.querySelectorAll<HTMLElement>('h1,h2,h3')]
  const passed = headings.filter((heading) => heading.offsetTop <= element.scrollTop + 24)
  const heading = passed[passed.length - 1] ?? headings[0]
  return heading?.id || [...(heading?.textContent?.trim() ?? '')].slice(0, 160).join('')
}
function flushProgress(keepalive = false) {
  clearTimeout(progressTimer); progressTimer = undefined
  if (progressSending && !keepalive) return scheduleProgress()
  const value = latestProgress ?? (keepalive ? inFlightProgress : undefined)
  if (!value) return
  if (latestProgress === value) latestProgress = undefined
  if (keepalive) { void updateProgress(value, true).catch(() => { /* The page is leaving; reading remains nonblocking. */ }); return }
  progressSending = true
  inFlightProgress = value
  void updateProgress(value).catch(() => {
    if (newestProgress === `${value.revisionId}:${value.observedAt}`) latestProgress = value
  }).finally(() => { if (inFlightProgress === value) inFlightProgress = undefined; progressSending = false; scheduleProgress() })
}
function scheduleProgress() { if (latestProgress && !progressTimer) progressTimer = setTimeout(flushProgress, 1000) }
function pageHide() { flushProgress(true) }
watch(() => props.lessonId, load)
onBeforeMount(() => { window.addEventListener('pagehide', pageHide); void load() })
onBeforeUnmount(() => { controller?.abort(); flushProgress(true); window.removeEventListener('pagehide', pageHide) })
</script>

<template>
  <section class="lesson-reader" aria-live="polite">
    <p v-if="loading" role="status">正在加载课程…</p>
    <div v-else-if="error" role="alert" class="notice"><p>{{ error }}</p><button type="button" @click="load">重试</button></div>
    <article v-else-if="lesson" class="reader-scroll" data-reader-scroll @scroll.passive="recordScroll">
      <header><p class="eyebrow">第 {{ lesson.version }} 版</p><h1>{{ lesson.title }}</h1><p>{{ lesson.summary }}</p></header>
      <MarkdownPreview :source="lesson.bodyMarkdown" />
      <section v-if="lesson.files.length" aria-labelledby="lesson-files-title"><h2 id="lesson-files-title">课程资料</h2><div class="files">
        <article v-for="file in lesson.files" :key="file.fileVersionId" class="file-card"><h3>{{ file.displayName }}</h3><p v-if="file.description">{{ file.description }}</p>
          <video v-if="file.browserPlayable" controls preload="metadata" :src="`/api/v1/files/${encodeURIComponent(file.fileVersionId)}/preview`"></video>
          <div class="file-actions"><a v-if="file.previewAvailable && !file.browserPlayable" :href="`/api/v1/files/${encodeURIComponent(file.fileVersionId)}/preview`" target="_blank" rel="noopener" :aria-label="`预览 ${file.displayName}`">在线预览</a><a v-if="file.policy === 'download'" :href="`/api/v1/files/${encodeURIComponent(file.fileVersionId)}/download`" :aria-label="`下载 ${file.displayName}`">下载</a></div>
        </article>
      </div></section>
      <section v-if="lesson.externalVideos.length" aria-labelledby="external-title"><h2 id="external-title">外部视频</h2><ExternalVideoFrame v-for="video in lesson.externalVideos" :key="video.id" :video="video" /></section>
    </article>
  </section>
</template>

<style scoped>
.lesson-reader{min-width:0}.reader-scroll{display:grid;gap:24px;max-height:calc(100vh - 150px);overflow:auto;padding-right:8px}.reader-scroll>header h1{margin:.3rem 0}.eyebrow{color:#2879b5;font-weight:800}.files{display:grid;gap:12px}.file-card{padding:16px;border:1px solid #d7e2ed;border-radius:10px;background:#fff}.file-card h3{margin-top:0}.file-card video{width:100%;max-height:520px;background:#102b4d}.file-actions{display:flex;gap:12px;margin-top:10px}.file-actions a{color:#176faf}.notice{padding:24px;border:1px solid #e0aaa6;border-radius:10px;background:#fff;color:#872e29}@media(max-width:760px){.reader-scroll{max-height:none;overflow:visible}}
</style>
