import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIError, registerUnauthorizedHandler, request, requestWithMeta } from './client'

describe('request', () => {
  afterEach(() => { vi.restoreAllMocks(); document.cookie = 'hl_csrf=; Max-Age=0; path=/'; registerUnauthorizedHandler(undefined) })
  it('sends credentials and the CSRF header on mutations', async () => {
    document.cookie = 'hl_csrf=csrf-value; path=/'
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
    await request('/auth/logout', { method: 'POST' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/logout', expect.objectContaining({ credentials: 'include', headers: expect.objectContaining({ Accept: 'application/json', 'X-CSRF-Token': 'csrf-value' }) }))
  })
  it('does not attach CSRF to safe requests and only adds content type for JSON', async () => {
    document.cookie = 'hl_csrf=csrf-value; path=/'
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ data: { id: 'u1' } })))
    await request('/auth/me')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/me', expect.objectContaining({ headers: { Accept: 'application/json' } }))
  })
  it('serializes JSON bodies and unwraps the data envelope', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ data: { id: 'u1' } })))
    await expect(request<{ id: string }>('/auth/login', { method: 'POST', json: { username: 'student01' } })).resolves.toEqual({ id: 'u1' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/login', expect.objectContaining({ body: JSON.stringify({ username: 'student01' }), headers: expect.objectContaining({ 'Content-Type': 'application/json' }) }))
  })
  it('preserves envelope metadata for callers that need it', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ data: ['u1'], meta: { nextCursor: 'next' } })))
    await expect(requestWithMeta<string[]>('/admin/students')).resolves.toEqual({ data: ['u1'], meta: { nextCursor: 'next' } })
  })
  it('maps server errors to APIError without exposing an unstable shape', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ error: { code: 'invalid_credentials', message: '用户名或密码错误', requestId: 'req-1' } }), { status: 401 }))
    await expect(request('/auth/login', { method: 'POST', json: {} })).rejects.toEqual(expect.objectContaining({ status: 401, code: 'invalid_credentials', requestId: 'req-1' }))
  })
  it('rejects malformed success envelopes with a stable APIError', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ result: {} })))
    await expect(request('/auth/me')).rejects.toEqual(expect.objectContaining({ status: 200, code: 'invalid_response', requestId: '' }))
  })
  it('clears the registered session handler after a 401', async () => {
    const clear = vi.fn(); registerUnauthorizedHandler(clear)
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ error: { code: 'unauthenticated', message: '请先登录', requestId: 'req-2' } }), { status: 401 }))
    await expect(request('/auth/me')).rejects.toBeInstanceOf(APIError)
    expect(clear).toHaveBeenCalledOnce()
  })
})