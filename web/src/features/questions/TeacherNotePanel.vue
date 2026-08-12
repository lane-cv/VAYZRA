<script setup lang="ts">
import { onBeforeUnmount,ref,watch } from 'vue'
import { APIError } from '../../api/client'
import { addTeacherNote, type TeacherNote } from './adminApi'
const props=defineProps<{questionId:string;notes:TeacherNote[]}>();const emit=defineEmits<{added:[TeacherNote]}>()
const body=ref(''),pending=ref(false),error=ref(''),requestId=ref('')
let generation=0,controller:AbortController|undefined
async function submit(){const value=body.value.trim();if(!value||pending.value)return;const token=generation,id=props.questionId,c=new AbortController();controller=c;pending.value=true;error.value='';requestId.value='';try{const note=await addTeacherNote(id,value,c.signal);if(token!==generation||id!==props.questionId)return;body.value='';emit('added',note)}catch(cause){if(token!==generation||id!==props.questionId||c.signal.aborted)return;error.value=cause instanceof Error?cause.message:'保存备注失败';requestId.value=cause instanceof APIError?cause.requestId:''}finally{if(token===generation)pending.value=false;if(controller===c)controller=undefined}}
watch(()=>props.questionId,()=>{generation+=1;controller?.abort();controller=undefined;body.value='';pending.value=false;error.value='';requestId.value=''})
onBeforeUnmount(()=>{generation+=1;controller?.abort()})
</script>
<template><aside class="notes" aria-labelledby="teacher-notes-title"><header><h2 id="teacher-notes-title">老师备注</h2><strong>仅老师可见</strong></header><ul><li v-for="note in notes" :key="note.id"><p>{{note.body}}</p><time :datetime="note.createdAt">{{new Date(note.createdAt).toLocaleString('zh-CN')}}</time></li></ul><form @submit.prevent="submit"><label>新增私密备注<textarea v-model="body" maxlength="20000" required /></label><p v-if="error" role="alert">{{error}}<span v-if="requestId">（支持编号：{{requestId}}）</span></p><button :disabled="pending">{{pending?'保存中…':'保存备注'}}</button></form></aside></template>
<style scoped>.notes{display:grid;gap:12px;padding:16px;border:1px solid var(--hl-border);border-radius:10px;background:var(--hl-surface-solid)}.notes header{display:flex;justify-content:space-between}.notes strong{color:#7a4d00}.notes ul{list-style:none;padding:0}.notes li{padding:8px;border-bottom:1px solid var(--hl-border)}.notes p{white-space:pre-wrap}.notes time{color:var(--hl-text-muted);font-size:.8rem}.notes label,.notes form{display:grid;gap:8px}.notes textarea{min-height:90px;background:var(--hl-surface-solid);color:var(--hl-text)}</style>
