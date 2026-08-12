<script setup lang="ts">
import { nextTick, onBeforeMount, onBeforeUnmount, ref, watch } from 'vue'
import { APIError } from '../../api/client'
import LessonReader from './LessonReader.vue'
import { browseCatalog, searchLessons } from './api'
import type { SearchResult, StudentCatalogKind, StudentCatalogNode } from './types'

const props = withDefaults(defineProps<{ lessonId?: string }>(), { lessonId: '' })
const grades = ref<StudentCatalogNode[]>([]), terms = ref<StudentCatalogNode[]>([]), subjects = ref<StudentCatalogNode[]>([]), chapters = ref<StudentCatalogNode[]>([]), lessons = ref<StudentCatalogNode[]>([])
const gradeId = ref(''), termId = ref(''), subjectId = ref(''), chapterId = ref('')
const loading = ref(false), error = ref(''), pathEmpty = ref('')
const query = ref(''), results = ref<SearchResult[]>([]), searching = ref(false)
let active: AbortController | undefined
let searchController: AbortController | undefined
let searchTimer: ReturnType<typeof setTimeout> | undefined
const catalogOpen = ref(false)
const menuTrigger = ref<HTMLButtonElement>()
const courseNav = ref<HTMLElement>()

async function load(kind: StudentCatalogKind, target: typeof grades, filters: Record<string, string> = {}) {
  active?.abort(); const controller = new AbortController(); active = controller; loading.value = true; error.value = ''; pathEmpty.value = ''
  try {
    const found = await browseCatalog(kind, filters, controller.signal)
    if (active !== controller) return
    target.value = found
    if (!target.value.length && kind !== 'grade') pathEmpty.value = kind === 'term' ? '这个年级暂时没有可学习的课程' : '当前选择下暂时没有可学习的课程'
  } catch (cause) { if (active === controller && !controller.signal.aborted) error.value = cause instanceof APIError ? cause.message : '学习目录加载失败' }
  finally { if (active === controller) loading.value = false }
}
function cancelCatalogLoad() { active?.abort(); active = undefined; loading.value = false }
async function selectGrade() { termId.value='';subjectId.value='';chapterId.value='';terms.value=[];subjects.value=[];chapters.value=[];lessons.value=[];syncFilterURL();if(gradeId.value) await load('term', terms, { gradeId: gradeId.value });else cancelCatalogLoad() }
async function selectTerm() { subjectId.value='';chapterId.value='';subjects.value=[];chapters.value=[];lessons.value=[];syncFilterURL();if(termId.value) await load('subject', subjects, { gradeId:gradeId.value,termId:termId.value });else cancelCatalogLoad() }
async function selectSubject() { chapterId.value='';chapters.value=[];lessons.value=[];syncFilterURL();if(subjectId.value) await load('chapter', chapters, { gradeId:gradeId.value,termId:termId.value,subjectId:subjectId.value });else cancelCatalogLoad() }
async function selectChapter() { lessons.value=[];syncFilterURL();if(chapterId.value) await load('lesson', lessons, { gradeId:gradeId.value,termId:termId.value,subjectId:subjectId.value,chapterId:chapterId.value });else cancelCatalogLoad() }
function highlight(text: string) {
  const needle = query.value.trim().toLocaleLowerCase(); if (!needle) return [{ text, match:false }]
  const lower = text.toLocaleLowerCase(), parts: { text:string;match:boolean }[]=[]; let offset=0,index=lower.indexOf(needle)
  while(index>=0){if(index>offset)parts.push({text:text.slice(offset,index),match:false});parts.push({text:text.slice(index,index+needle.length),match:true});offset=index+needle.length;index=lower.indexOf(needle,offset)}
  if(offset<text.length)parts.push({text:text.slice(offset),match:false});return parts
}
function openCatalog() { catalogOpen.value=true;void nextTick(()=>courseNav.value?.querySelector<HTMLElement>('select,a,button')?.focus()) }
function closeCatalog(restore=false) { catalogOpen.value=false;if(restore)void nextTick(()=>menuTrigger.value?.focus()) }
function filterParams() {
  const params = new URLSearchParams()
  if (gradeId.value) params.set('gradeId', gradeId.value)
  if (termId.value) params.set('termId', termId.value)
  if (subjectId.value) params.set('subjectId', subjectId.value)
  if (chapterId.value) params.set('chapterId', chapterId.value)
  return params
}
function syncFilterURL() {
  const params = filterParams()
  window.history.replaceState(window.history.state, '', `${window.location.pathname}${params.size ? `?${params}` : ''}${window.location.hash}`)
}
function lessonHref(lessonId: string) {
  const params = filterParams()
  return `/student/learning/${encodeURIComponent(lessonId)}${params.size ? `?${params}` : ''}`
}
function documentKeydown(event: KeyboardEvent) { if(event.key==='Escape'&&catalogOpen.value)closeCatalog(true) }
function trapCatalog(event: KeyboardEvent) {
  if(event.key!=='Tab'||!catalogOpen.value||!courseNav.value)return
  const controls=[...courseNav.value.querySelectorAll<HTMLElement>('select:not(:disabled),a[href],button:not(:disabled)')];if(!controls.length)return
  const first=controls[0],last=controls[controls.length-1]
  if(event.shiftKey&&document.activeElement===first){event.preventDefault();last.focus()}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first.focus()}
}
watch(query, () => {
  clearTimeout(searchTimer); searchController?.abort(); results.value=[]
  if (query.value.trim().length < 2) return
  searchTimer=setTimeout(()=>{void runSearch()},250)
})
async function runSearch() { const controller=new AbortController();searchController=controller;searching.value=true;try{const found=await searchLessons(query.value,controller.signal);if(searchController!==controller)return;results.value=found.filter((item)=>!gradeId.value||item.gradeId===gradeId.value).filter((item)=>!termId.value||item.termId===termId.value).filter((item)=>!subjectId.value||item.subjectId===subjectId.value).filter((item)=>!chapterId.value||item.chapterId===chapterId.value)}catch(cause){if(searchController===controller&&!controller.signal.aborted)error.value=cause instanceof APIError?cause.message:'搜索失败'}finally{if(searchController===controller)searching.value=false} }
async function restoreCatalog() {
  const params = new URLSearchParams(window.location.search)
  const requested = { gradeId: params.get('gradeId') ?? '', termId: params.get('termId') ?? '', subjectId: params.get('subjectId') ?? '', chapterId: params.get('chapterId') ?? '' }
  await load('grade', grades)
  if (!grades.value.some((item) => item.id === requested.gradeId)) return syncFilterURL()
  gradeId.value = requested.gradeId
  await load('term', terms, { gradeId: gradeId.value })
  if (!terms.value.some((item) => item.id === requested.termId)) return syncFilterURL()
  termId.value = requested.termId
  await load('subject', subjects, { gradeId: gradeId.value, termId: termId.value })
  if (!subjects.value.some((item) => item.id === requested.subjectId)) return syncFilterURL()
  subjectId.value = requested.subjectId
  await load('chapter', chapters, { gradeId: gradeId.value, termId: termId.value, subjectId: subjectId.value })
  if (!chapters.value.some((item) => item.id === requested.chapterId)) return syncFilterURL()
  chapterId.value = requested.chapterId
  await load('lesson', lessons, { gradeId: gradeId.value, termId: termId.value, subjectId: subjectId.value, chapterId: chapterId.value })
  syncFilterURL()
}
onBeforeMount(() => { document.addEventListener('keydown',documentKeydown);void restoreCatalog() })
onBeforeUnmount(() => { document.removeEventListener('keydown',documentKeydown);active?.abort();searchController?.abort();clearTimeout(searchTimer) })
</script>

<template>
  <section class="learning" aria-labelledby="learning-title"><header><p class="eyebrow">学习空间</p><h1 id="learning-title">课程学习</h1><p>按目录查找课程，或搜索当前可访问的已发布内容。</p></header>
    <div class="filters"><label>年级<select v-model="gradeId" aria-label="年级筛选" @change="selectGrade"><option value="">请选择</option><option v-for="item in grades" :key="item.id" :value="item.id">{{ item.name }}</option></select></label><label>学期<select v-model="termId" aria-label="学期筛选" :disabled="!terms.length" @change="selectTerm"><option value="">请选择</option><option v-for="item in terms" :key="item.id" :value="item.id">{{ item.name }}</option></select></label><label>学科<select v-model="subjectId" aria-label="学科筛选" :disabled="!subjects.length" @change="selectSubject"><option value="">请选择</option><option v-for="item in subjects" :key="item.id" :value="item.id">{{ item.name }}</option></select></label><label class="search">搜索<input v-model="query" aria-label="搜索课程" type="search" placeholder="至少输入两个字"></label></div>
    <p v-if="loading" role="status">正在加载学习目录…</p><p v-if="error" role="alert">{{ error }}</p><p v-if="pathEmpty" class="empty">{{ pathEmpty }}</p>
    <div v-if="query.trim().length >= 2" class="search-results"><p v-if="searching">正在搜索…</p><a v-for="item in results" :key="item.revisionId" :href="lessonHref(item.lessonId)"><strong>{{ item.title }}</strong><span><template v-for="(part,index) in highlight(item.snippet)" :key="index"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span></a><p v-if="!searching && !results.length">没有找到可访问的课程</p></div>
    <button ref="menuTrigger" class="course-menu" type="button" aria-label="打开课程目录" :aria-expanded="catalogOpen" @click="openCatalog">课程目录</button><button v-if="catalogOpen" class="course-scrim" aria-label="关闭课程目录" @click="closeCatalog(true)"></button>
    <div class="workspace"><nav ref="courseNav" class="course-nav" :class="{ open:catalogOpen }" aria-label="课程目录" :role="catalogOpen?'dialog':undefined" :aria-modal="catalogOpen?'true':undefined" @keydown="trapCatalog"><button class="course-close" type="button" @click="closeCatalog(true)">关闭</button><label>章节<select v-model="chapterId" aria-label="章节筛选" :disabled="!chapters.length" @change="selectChapter"><option value="">请选择章节</option><option v-for="item in chapters" :key="item.id" :value="item.id">{{ item.name }}</option></select></label><ul><li v-for="item in lessons" :key="item.id"><a :href="lessonHref(item.lessonId || item.id)">{{ item.name }}</a></li></ul></nav><div class="reader-panel" :inert="catalogOpen||undefined"><LessonReader v-if="props.lessonId" :lesson-id="props.lessonId" /><div v-else class="reader-empty"><h2>选择一节课程开始学习</h2><p>课程正文、资料和视频会显示在这里。</p></div></div></div>
  </section>
</template>

<style scoped>
.learning{display:grid;gap:20px}.learning header h1{margin:.25rem 0}.eyebrow{margin:0;color:var(--hl-primary-strong);font-weight:800}.filters{display:grid;grid-template-columns:repeat(3,minmax(120px,1fr)) minmax(220px,2fr);gap:12px}.filters label,.course-nav label{display:grid;gap:6px}.filters select,.filters input,.course-nav select{padding:9px;border:1px solid var(--hl-border-strong);border-radius:7px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit}.workspace{display:grid;grid-template-columns:260px minmax(0,1fr);gap:22px}.course-nav{padding:16px;border:1px solid var(--hl-border);border-radius:10px;background:var(--hl-surface-solid)}.course-nav ul{display:grid;gap:6px;padding:0;list-style:none}.course-nav a,.search-results a{color:var(--hl-primary-strong);text-decoration:none}.reader-empty,.empty{padding:28px;border:1px dashed var(--hl-border-strong);border-radius:10px;background:var(--hl-surface-solid);text-align:center}.search-results{display:grid;gap:8px}.search-results a{display:grid;gap:4px;padding:12px;border:1px solid var(--hl-border);border-radius:8px;background:var(--hl-surface-solid)}.search-results span{color:var(--hl-text-muted)}.course-menu,.course-close,.course-scrim{display:none}@media(max-width:850px){.filters{grid-template-columns:1fr 1fr}.workspace{grid-template-columns:1fr}.course-menu{display:block;justify-self:start;padding:9px 13px;border:1px solid var(--hl-border-strong);border-radius:7px;background:var(--hl-surface-solid);color:var(--hl-text)}.course-nav{position:fixed;z-index:31;inset:0 auto 0 0;width:min(320px,85vw);transform:translateX(-105%);transition:transform .2s}.course-nav.open{transform:none}.course-close{display:block;margin-left:auto}.course-scrim{display:block;position:fixed;z-index:30;inset:0;border:0;background:#07182888}}@media(max-width:560px){.filters{grid-template-columns:1fr}}@media(prefers-reduced-motion:reduce){.course-nav{transition:none}}
</style>
