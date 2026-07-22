import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useNotificationStore } from './notifications'

function countResponse(count: number) { return new Response(JSON.stringify({ data: { count } })) }
function listeners() {
  const addDocument = vi.spyOn(document, 'addEventListener'), removeDocument = vi.spyOn(document, 'removeEventListener')
  const addWindow = vi.spyOn(window, 'addEventListener'), removeWindow = vi.spyOn(window, 'removeEventListener')
  return { addDocument, removeDocument, addWindow, removeWindow }
}
describe('notification polling lifecycle', () => {
  beforeEach(() => { setActivePinia(createPinia()); vi.useFakeTimers(); vi.stubGlobal('fetch', vi.fn().mockResolvedValue(countResponse(4))); vi.spyOn(document, 'hidden', 'get').mockReturnValue(false) })
  afterEach(() => { useNotificationStore().stop(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it('refreshes immediately and has exactly one 15 second timer for duplicate starts', async () => {
    const store = useNotificationStore(); store.start('u1'); store.start('u1'); await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))
    expect(vi.getTimerCount()).toBe(1); await vi.advanceTimersByTimeAsync(15_000); expect(fetch).toHaveBeenCalledTimes(2); expect(store.unreadCount).toBe(4)
  })
  it('pauses while hidden and coalesces visibility and focus refreshes', async () => {
    let hidden = false; vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
    const store = useNotificationStore(); store.start('u1'); await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1)); await store.refresh()
    hidden = true; document.dispatchEvent(new Event('visibilitychange')); expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(30_000); expect(fetch).toHaveBeenCalledTimes(1)
    hidden = false; document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event('focus')); await vi.runAllTicks(); await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2)); expect(vi.getTimerCount()).toBe(1)
  })
  it('coalesces adjacent-task visibility and focus wakes even after a fast request settles', async () => {
    let hidden = true; vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
    const store = useNotificationStore(); store.start('u1'); await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1)); await store.refresh()
    hidden = false; document.dispatchEvent(new Event('visibilitychange')); await vi.runAllTicks(); await Promise.resolve()
    expect(fetch).toHaveBeenCalledTimes(2)
    window.dispatchEvent(new Event('focus')); await vi.runAllTicks(); expect(fetch).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(250); window.dispatchEvent(new Event('focus')); await vi.runAllTicks(); expect(fetch).toHaveBeenCalledTimes(3)
  })
  it('does the one immediate refresh when started hidden but creates no timer', async () => {
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    const store = useNotificationStore(); store.start('u1'); await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1)); expect(vi.getTimerCount()).toBe(0)
  })
  it('caps only the visual badge while retaining the numeric count', async () => {
    vi.mocked(fetch).mockResolvedValue(countResponse(120)); const store = useNotificationStore(); store.start('u1')
    await vi.waitFor(() => expect(store.unreadCount).toBe(120)); expect(store.badgeText).toBe('99+')
  })
  it('never overlaps a slow count request and aborts/cleans on user switch and stop', async () => {
    const observed: AbortSignal[] = []; vi.mocked(fetch).mockImplementation((_url, init) => { observed.push(init?.signal as AbortSignal); return new Promise(() => {}) })
    const hooks = listeners(), store = useNotificationStore(); store.start('u1'); await vi.runAllTicks(); await vi.advanceTimersByTimeAsync(45_000)
    expect(fetch).toHaveBeenCalledTimes(1); const first = observed[0]; store.start('u2'); expect(first.aborted).toBe(true); await vi.runAllTicks(); expect(fetch).toHaveBeenCalledTimes(2)
    const second = observed[1]; store.stop(); expect(second.aborted).toBe(true); expect(vi.getTimerCount()).toBe(0)
    expect(hooks.removeDocument).toHaveBeenCalledWith('visibilitychange', expect.any(Function)); expect(hooks.removeWindow).toHaveBeenCalledWith('focus', expect.any(Function))
  })
  it('stops the lifecycle when the global client reports a 401', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ error: { code: 'unauthenticated', message: 'expired', requestId: 'r1' } }), { status: 401 }))
    const store = useNotificationStore(); store.start('u1'); await vi.waitFor(() => expect(store.activeUserId).toBeUndefined()); expect(vi.getTimerCount()).toBe(0)
  })
  it('aborts stale list pages and appends only the current cursor result', async () => {
    let firstSignal: AbortSignal | undefined
    vi.mocked(fetch).mockImplementationOnce((_url, init) => { firstSignal = init?.signal as AbortSignal; return new Promise(() => {}) })
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: [{ id:'n2',kind:'qa_replied',title:'New',summary:'Summary',targetType:'qa_thread',targetId:'q2',targetPath:'/student/questions/q2',createdAt:'2026-07-22T00:00:00Z' }], meta: { nextCursor: 'c2' } })))
    const store = useNotificationStore(); void store.list(); await vi.runAllTicks(); await store.list('c1')
    expect(firstSignal?.aborted).toBe(true); expect(store.items.map((entry) => entry.id)).toEqual(['n2']); expect(store.nextCursor).toBe('c2')
  })
})
