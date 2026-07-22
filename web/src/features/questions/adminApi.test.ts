import { beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { addTeacherNote, changeQuestionStatus, getAdminQuestion, listAdminQuestions, newAdminIdempotencyKey, replyToQuestion } from './adminApi'

describe('admin question api', () => {
  beforeEach(() => { document.cookie = 'hl_csrf=csrf; path=/'; vi.stubGlobal('fetch', vi.fn().mockImplementation(()=>Promise.resolve(new Response(JSON.stringify({ data: { thread: { id: 'q1', studentId: 's1', version: 2 }, message: { id: 'm1' } } }), { status: 200 })))) })
  it('strictly encodes every queue filter and cursor', async () => {
    await listAdminQuestions({ status: 'in_progress', studentId: '11111111-1111-4111-8111-111111111111', from: '2026-07-01T00:00:00Z', to: '2026-07-20T00:00:00Z', limit: 25 }, 'a+b/=')
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/admin/questions?status=in_progress&studentId=11111111-1111-4111-8111-111111111111&from=2026-07-01T00%3A00%3A00Z&to=2026-07-20T00%3A00%3A00Z&cursor=a%2Bb%2F%3D&limit=25')
  })
  it('aligns mutation bodies and idempotency with the backend contract', async () => {
    await replyToQuestion('q/1', 'answer', [{ fileVersionId: 'v1', sortPosition: 0 }], 'stable-key', 4)
    await changeQuestionStatus('q/1', 'completed', 5)
    await addTeacherNote('q/1', 'private')
    const calls = vi.mocked(fetch).mock.calls
    expect(calls.map(([url]) => url)).toEqual(['/api/v1/admin/questions/q%2F1/messages','/api/v1/admin/questions/q%2F1/status','/api/v1/admin/questions/q%2F1/notes'])
    expect(new Headers(calls[0][1]?.headers).get('Idempotency-Key')).toBe('stable-key')
    expect(calls.map(([,init]) => JSON.parse(String(init?.body)))).toEqual([{ body:'answer',attachments:[{fileVersionId:'v1',sortPosition:0}],expectedVersion:4 },{status:'completed',expectedVersion:5},{body:'private'}])
    expect(newAdminIdempotencyKey()).toMatch(/^[0-9a-f-]{36}$/)
  })
  it('retains server conflict codes and request IDs for explicit reload handling', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ error: { code:'thread_conflict',message:'问题已被更新，请刷新后重试',requestId:'req-1' } }),{status:409}))
    await expect(changeQuestionStatus('q1','completed',2)).rejects.toMatchObject({status:409,code:'thread_conflict',requestId:'req-1'} satisfies Partial<APIError>)
  })
  it('reads the actual admin DTO (studentId and notes, not a fabricated student object)', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({data:{thread:{id:'q1',studentId:'s1'},messages:[],notes:[{id:'n1',authorUserId:'a1',body:'private',createdAt:'now'}]}})))
    await expect(getAdminQuestion('q1')).resolves.toMatchObject({thread:{studentId:'s1'},notes:[{body:'private'}]})
  })
})
