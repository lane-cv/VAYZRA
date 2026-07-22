import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { listNotifications, markAllNotificationsRead, markNotificationRead, unreadNotificationCount } from './api'

describe('notification api', () => {
  beforeEach(() => { document.cookie = 'hl_csrf=csrf; path=/'; vi.stubGlobal('fetch', vi.fn()) })
  afterEach(() => vi.unstubAllGlobals())

  it('uses only the canonical cursor and limit query', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ data: [], meta: { nextCursor: 'next' } })))
    await expect(listNotifications('a+b/=', 20)).resolves.toEqual({ items: [], nextCursor: 'next' })
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/notifications?cursor=a%2Bb%2F%3D&limit=20')
  })

  it('matches the count envelope and sends exact empty JSON mutations', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: { count: 123 } })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: {} })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { count: 3 } })))
    await expect(unreadNotificationCount()).resolves.toBe(123)
    await markNotificationRead('id /?')
    await expect(markAllNotificationsRead()).resolves.toBe(3)
    expect(vi.mocked(fetch).mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/notifications/unread-count', '/api/v1/notifications/id%20%2F%3F/read', '/api/v1/notifications/read-all',
    ])
    for (const call of vi.mocked(fetch).mock.calls.slice(1)) expect(JSON.parse(String(call[1]?.body))).toEqual({})
  })
  it('forwards abort signals for both notification mutations', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: {} })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { count: 1 } })))
    const one = new AbortController(), all = new AbortController()
    await markNotificationRead('n1', one.signal)
    await markAllNotificationsRead(all.signal)
    expect(vi.mocked(fetch).mock.calls[0][1]?.signal).toBe(one.signal)
    expect(vi.mocked(fetch).mock.calls[1][1]?.signal).toBe(all.signal)
  })
  it('rejects a non-object or non-empty mark-read data body', async () => {
    for (const data of [[], { unexpected: true }, null, '']) {
      vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data })))
      await expect(markNotificationRead('n1')).rejects.toMatchObject({ code: 'invalid_response' })
    }
  })
})
