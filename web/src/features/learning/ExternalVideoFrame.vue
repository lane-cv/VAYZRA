<script setup lang="ts">
import type { StudentVideo } from './types'
defineProps<{ video: StudentVideo }>()
</script>

<template>
  <article class="external-video">
    <h3>{{ video.title }}</h3><p v-if="video.description">{{ video.description }}</p>
    <iframe :src="video.url" :title="video.title" sandbox="allow-scripts allow-forms allow-presentation" referrerpolicy="no-referrer" allow="fullscreen; picture-in-picture" loading="lazy"></iframe>
    <p><a :href="video.url" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer">无法播放？在新窗口打开</a></p>
  </article>
</template>

<style scoped>
.external-video{padding:16px;border:1px solid var(--hl-border);border-radius:10px;background:var(--hl-surface-solid)}.external-video iframe{width:100%;min-height:320px;border:0;border-radius:8px;background:#102b4d}.external-video h3{margin-top:0}@media(max-width:650px){.external-video iframe{min-height:220px}}
</style>
