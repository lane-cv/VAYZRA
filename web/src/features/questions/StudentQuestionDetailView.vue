<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import QuestionAttachmentUploader from './QuestionAttachmentUploader.vue'
import QuestionTimeline from './QuestionTimeline.vue'
import { addStudentMessage, getStudentQuestion, listStudentMessages, newIdempotencyKey } from './studentApi'
import type { AttachmentInput, QuestionDetail, QuestionMessage, QuestionStatus } from './types'
const props = withDefaults(defineProps<{ questionId: string; userId?: string }>(), { userId: '' })
const session = props.userId ? undefined : useSessionStore(), detail = ref<QuestionDetail>(), loading = ref(false), error = ref(''), requestId = ref(''), reply = ref(''), attachments = ref<AttachmentInput[]>([]), uploadsPending = ref(false), submitting = ref(false), errorBox = ref<HTMLElement>(), uploaderKey = ref(0)
let controller: AbortController | undefined, moreController: AbortController | undefined, generation = 0, mutationKey = '', mutationFingerprint = ''
const labels: Record<QuestionStatus,string> = { pending:'待老师处理', in_progress:'老师处理中', waiting_student:'等待我回复', completed:'已完成' }
const chars = (value:string) => Array.from(value.trim()).length
function current(token:number,id:string){return token===generation&&id===props.questionId}
function resetThread() { generation+=1;controller?.abort();moreController?.abort();detail.value=undefined;reply.value='';attachments.value=[];uploadsPending.value=false;submitting.value=false;error.value='';requestId.value='';mutationKey='';mutationFingerprint='';uploaderKey.value+=1 }
async function load() {
  controller?.abort(); moreController?.abort(); const token=++generation,id=props.questionId,requestController=new AbortController(); controller=requestController; loading.value=true; error.value=''; requestId.value=''
  try { const result=await getStudentQuestion(id,requestController.signal); if(current(token,id)) detail.value=result }
  catch(cause){if(requestController.signal.aborted||!current(token,id))return; showError(cause,'加载问题失败')}
  finally{if(current(token,id))loading.value=false}
}
async function more() {
  if (!detail.value?.nextMessageCursor || loading.value) return
  const token=generation,id=props.questionId,cursor=detail.value.nextMessageCursor,requestController=new AbortController();moreController?.abort();moreController=requestController;loading.value=true
  try { const page=await listStudentMessages(id,cursor,100,requestController.signal); if(current(token,id)&&detail.value?.thread.id===id) detail.value={...detail.value,messages:merge(detail.value.messages,page.items),nextMessageCursor:page.nextCursor} }
  catch(cause){if(!requestController.signal.aborted&&current(token,id))showError(cause,'加载更多消息失败')} finally{if(current(token,id))loading.value=false}
}
async function submit() {
  if(submitting.value)return; error.value='';requestId.value=''
  if(chars(reply.value)<1||chars(reply.value)>20000) { error.value='追问内容需为 1–20,000 个字符'; await focusError(); return }
  if(uploadsPending.value){error.value='请等待附件完成安全检查';await focusError();return}
  const id=props.questionId,token=generation,currentFingerprint=JSON.stringify([id,reply.value.trim(),attachments.value.map((attachment)=>attachment.fileVersionId)])
  if(!mutationKey||mutationFingerprint!==currentFingerprint){mutationKey=newIdempotencyKey();mutationFingerprint=currentFingerprint}
  submitting.value=true
  try { const result=await addStudentMessage(id,{body:reply.value.trim(),attachments:attachments.value},mutationKey);if(!current(token,id))return;if(detail.value) detail.value={thread:result.thread,messages:merge(detail.value.messages,result.messages),nextMessageCursor:detail.value.nextMessageCursor};reply.value='';attachments.value=[];mutationKey='';mutationFingerprint='';uploaderKey.value+=1 }
  catch(cause){if(!current(token,id))return;showError(cause,'追问提交失败');await focusError()} finally{if(current(token,id))submitting.value=false}
}
function merge(first:QuestionMessage[],second:QuestionMessage[]){const byID=new Map(first.map((item)=>[item.id,item]));second.forEach((item)=>byID.set(item.id,item));return [...byID.values()]}
function showError(cause:unknown,fallback:string){error.value=cause instanceof Error?cause.message:fallback;requestId.value=cause instanceof APIError?cause.requestId:''}
async function focusError(){await nextTick();errorBox.value?.focus()}
onMounted(()=>void load());watch(()=>props.questionId,()=>{resetThread();void load()});onBeforeUnmount(()=>{generation+=1;controller?.abort();moreController?.abort()})
</script>
<template>
  <section class="detail" aria-labelledby="question-title">
    <RouterLink to="/student/questions">← 返回我的问题</RouterLink>
    <p v-if="loading && !detail" role="status">正在加载问题…</p>
    <div v-else-if="error && !detail" role="alert"><p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p><button type="button" aria-label="重试加载问题" @click="load">重试</button></div>
    <template v-else-if="detail">
      <header><div><h1 id="question-title">{{ detail.thread.title }}</h1><p>{{ labels[detail.thread.status] }}</p></div></header>
      <QuestionTimeline :messages="detail.messages" />
      <button v-if="detail.nextMessageCursor" type="button" :disabled="loading" @click="more">加载更多消息</button>
      <section class="reply" aria-labelledby="followup-title"><h2 id="followup-title">继续追问</h2><p v-if="detail.thread.status==='completed'">该问题已完成，但你仍可继续追问；提交后将重新进入待处理状态。</p>
        <form @submit.prevent="submit"><label>追问内容<textarea v-model="reply" aria-label="追问内容" maxlength="20000" rows="6"></textarea></label><small>{{ Array.from(reply).length }}/20000</small>
          <QuestionAttachmentUploader :key="uploaderKey" :user-id="props.userId || session?.user?.id || ''" :disabled="submitting" @update:attachments="attachments=$event" @pending-change="uploadsPending=$event" />
          <p v-if="error" ref="errorBox" role="alert" tabindex="-1">{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p><button type="submit" :disabled="submitting||uploadsPending">{{ submitting?'正在提交…':'提交追问' }}</button>
        </form>
      </section>
    </template>
  </section>
</template>
<style scoped>.detail{display:grid;gap:20px;max-width:900px}.detail>a{color:#176faf}.detail>header{display:flex;justify-content:space-between}.detail h1{margin:.2rem 0}.detail header p{color:#176faf;font-weight:700}.reply{margin-top:8px;padding:20px;border:1px solid #dbe4f0;border-radius:12px;background:#fff}.reply form{display:grid;gap:10px}.reply label{display:grid;gap:7px;font-weight:700}.reply textarea{padding:10px;border:1px solid #b9cadb;border-radius:8px;font:inherit;line-height:1.6}.reply small{justify-self:end}.reply button{justify-self:start;padding:9px 15px}[role=alert]{color:#a33731}</style>
