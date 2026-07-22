import { request, requestWithMeta } from '../../api/client'
import type { AttachmentInput, QuestionDetail, QuestionMessage, QuestionStatus, QuestionThread } from './types'

export type AdminQuestionThread = QuestionThread & { studentId: string }
export type TeacherNote = { id: string; authorUserId: string; body: string; createdAt: string }
export type AdminQuestionDetail = Omit<QuestionDetail, 'thread'> & { thread: AdminQuestionThread; notes: TeacherNote[] }
export type AdminThreadPage = { items: AdminQuestionThread[]; nextCursor?: string }
export type AdminQuestionFilters = { status?: QuestionStatus; studentId?: string; from?: string; to?: string; limit?: number }
export type AdminMutationResult = { thread: AdminQuestionThread; message: QuestionMessage }

export async function listAdminQuestions(filters: AdminQuestionFilters, cursor?: string, signal?: AbortSignal): Promise<AdminThreadPage> {
  const query = new URLSearchParams()
  if (filters.status) query.set('status', filters.status)
  if (filters.studentId) query.set('studentId', filters.studentId)
  if (filters.from) query.set('from', filters.from)
  if (filters.to) query.set('to', filters.to)
  if (cursor) query.set('cursor', cursor)
  if (filters.limit !== undefined) query.set('limit', String(filters.limit))
  const result=await requestWithMeta<AdminQuestionThread[]>(`/admin/questions${query.size?`?${query.toString()}`:''}`,{signal})
  return {items:result.data,nextCursor:typeof result.meta?.nextCursor==='string'?result.meta.nextCursor:undefined}
}
export function getAdminQuestion(id:string,options:{cursor?:string;limit?:number}={},signal?:AbortSignal):Promise<AdminQuestionDetail>{const query=new URLSearchParams();if(options.cursor)query.set('cursor',options.cursor);if(options.limit!==undefined)query.set('limit',String(options.limit));return request(`/admin/questions/${encodeURIComponent(id)}${query.size?`?${query.toString()}`:''}`,{signal})}
export function replyToQuestion(id:string,body:string,attachments:AttachmentInput[],key:string,expectedVersion:number,signal?:AbortSignal):Promise<AdminMutationResult>{return request(`/admin/questions/${encodeURIComponent(id)}/messages`,{method:'POST',headers:{'Idempotency-Key':key},json:{body,attachments,expectedVersion},signal})}
export function changeQuestionStatus(id:string,status:QuestionStatus,expectedVersion:number,signal?:AbortSignal):Promise<AdminQuestionThread>{return request(`/admin/questions/${encodeURIComponent(id)}/status`,{method:'POST',json:{status,expectedVersion},signal})}
export function addTeacherNote(id:string,body:string,signal?:AbortSignal):Promise<TeacherNote>{return request(`/admin/questions/${encodeURIComponent(id)}/notes`,{method:'POST',json:{body},signal})}
export function newAdminIdempotencyKey():string{return crypto.randomUUID()}
