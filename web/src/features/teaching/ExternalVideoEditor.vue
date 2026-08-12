<script setup lang="ts">
import type { ExternalVideo } from './types'
import { uuidV4 } from '../../utils/uuid'

const props = defineProps<{ modelValue: ExternalVideo[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: ExternalVideo[]] }>()
function update(index: number, field: keyof Pick<ExternalVideo, 'url' | 'title' | 'description'>, value: string) {
  emit('update:modelValue', props.modelValue.map((video, current) => current === index ? { ...video, [field]: value } : video))
}
function add() {
  emit('update:modelValue', [...props.modelValue, { id: uuidV4(), url: '', title: '', description: '', sortKey: (props.modelValue.length + 1) * 10 }])
}
function remove(index: number) { emit('update:modelValue', props.modelValue.filter((_, current) => current !== index)) }
</script>

<template>
  <section class="videos" aria-labelledby="external-videos-title">
    <header><div><h2 id="external-videos-title">外部视频</h2><p>仅填写受信任的 HTTPS 视频页面链接。</p></div><button type="button" @click="add">添加视频</button></header>
    <div v-for="(video, index) in modelValue" :key="video.id" class="video-row">
      <label>标题<input :value="video.title" :aria-label="`视频 ${index + 1} 标题`" maxlength="160" @input="update(index, 'title', ($event.target as HTMLInputElement).value)"></label>
      <label>HTTPS 地址<input :value="video.url" :aria-label="`视频 ${index + 1} 地址`" type="url" inputmode="url" @input="update(index, 'url', ($event.target as HTMLInputElement).value)"></label>
      <label>说明<textarea :value="video.description" :aria-label="`视频 ${index + 1} 说明`" maxlength="500" rows="2" @input="update(index, 'description', ($event.target as HTMLTextAreaElement).value)"></textarea></label>
      <button type="button" :aria-label="`删除视频 ${index + 1}`" @click="remove(index)">删除</button>
    </div>
  </section>
</template>

<style scoped>
.videos{display:grid;gap:12px}.videos header{display:flex;justify-content:space-between;gap:16px;align-items:center}.videos h2,.videos p{margin:.2rem 0}.video-row{display:grid;grid-template-columns:1fr 2fr;gap:10px;padding:14px;border:1px solid var(--hl-border);border-radius:10px;background:var(--hl-surface-solid)}.video-row label{display:grid;gap:6px}.video-row label:nth-child(3){grid-column:1/-1}.video-row input,.video-row textarea{padding:8px;border:1px solid var(--hl-border-strong);border-radius:7px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit}.video-row>button{justify-self:end}@media(max-width:700px){.video-row{grid-template-columns:1fr}}
</style>
