import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import LessonReader from './LessonReader.vue'

const lessonId = '11111111-1111-4111-8111-111111111111'
const revisionId = '22222222-2222-4222-8222-222222222222'
function response(data: unknown, status = 200) { return new Response(JSON.stringify({ data }), { status }) }

describe('LessonReader', () => {
  const mounted: ReturnType<typeof mount>[] = []
  function mountReader() { const wrapper = mount(LessonReader, { props: { lessonId } }); mounted.push(wrapper); return wrapper }
  beforeEach(() => {
    vi.useFakeTimers()
    document.cookie = 'hl_csrf=csrf-value; path=/'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({
      lessonId, revisionId, version: 2, title: '牛顿定律', summary: '理解惯性', bodyMarkdown: '# 第一节\n\n$x=vt$', sortKey: 10, publishedAt: '2026-07-21T00:00:00Z', progress: null,
      files: [
        { fileVersionId: 'f-video', policy: 'preview', displayName: '实验.mp4', description: '', sortPosition: 10, detectedMime: 'video/mp4', browserPlayable: true, previewAvailable: true },
        { fileVersionId: 'f-pdf', policy: 'download', displayName: '讲义.pdf', description: '', sortPosition: 20, detectedMime: 'application/pdf', browserPlayable: false, previewAvailable: true },
      ],
      externalVideos: [{ id: 'v1', url: 'https://video.example/1', title: '外部演示', description: '', sortKey: 10 }],
    })))
  })
  afterEach(() => { mounted.splice(0).forEach((wrapper) => wrapper.unmount()); vi.useRealTimers(); vi.unstubAllGlobals() })

  it('renders safe lesson content and only authorized file actions', async () => {
    const wrapper = mountReader()
    await flushPromises()
    expect(wrapper.get('[aria-label="课程正文预览"]').html()).toContain('katex')
    expect(wrapper.get('video').attributes('src')).toBe('/api/v1/files/f-video/preview')
    expect(wrapper.get('a[aria-label="预览 讲义.pdf"]').attributes('href')).toBe('/api/v1/files/f-pdf/preview')
    expect(wrapper.get('a[aria-label="下载 讲义.pdf"]').attributes('href')).toBe('/api/v1/files/f-pdf/download')
    expect(wrapper.find('a[aria-label="下载 实验.mp4"]').exists()).toBe(false)
    expect(wrapper.get('iframe').attributes('sandbox')).not.toContain('allow-same-origin')
  })

  it('throttles reading progress to one write per second and flushes the newest state on pagehide', async () => {
    const wrapper = mountReader()
    await flushPromises()
    const reader = wrapper.get('[data-reader-scroll]')
    Object.defineProperties(reader.element, { scrollHeight: { value: 1000 }, clientHeight: { value: 200 }, scrollTop: { value: 400, writable: true } })
    await reader.trigger('scroll')
    await reader.trigger('scroll')
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[1][1]?.body))).toMatchObject({ revisionId, viewed: true, scrollRatio: 0.5 })
    ;(reader.element as HTMLElement).scrollTop = 640
    await reader.trigger('scroll')
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[2][1]?.body)).scrollRatio).toBe(0.8)
  })

  it('retries only the newest reading state after a progress failure', async () => {
    const wrapper = mountReader()
    await flushPromises()
    const reader = wrapper.get('[data-reader-scroll]')
    Object.defineProperties(reader.element, { scrollHeight: { value: 1000 }, clientHeight: { value: 200 }, scrollTop: { value: 200, writable: true } })
    vi.mocked(fetch).mockRejectedValueOnce(new Error('offline'))
    await reader.trigger('scroll')
    await vi.advanceTimersByTimeAsync(1000); await flushPromises()
    ;(reader.element as HTMLElement).scrollTop = 600
    await reader.trigger('scroll')
    await vi.advanceTimersByTimeAsync(1000); await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[2][1]?.body)).scrollRatio).toBe(0.75)
  })

  it('keepalive-flushes the in-flight latest state when the page leaves', async () => {
    const wrapper = mountReader()
    await flushPromises()
    const reader = wrapper.get('[data-reader-scroll]')
    Object.defineProperties(reader.element, { scrollHeight: { value: 1000 }, clientHeight: { value: 200 }, scrollTop: { value: 480, writable: true } })
    let resolveProgress!: (value: Response) => void
    vi.mocked(fetch).mockReturnValueOnce(new Promise<Response>((resolve) => { resolveProgress = resolve }))
    await reader.trigger('scroll')
    await vi.advanceTimersByTimeAsync(1000); await flushPromises()
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(vi.mocked(fetch).mock.calls[2][1]?.keepalive).toBe(true)
    expect(vi.mocked(fetch).mock.calls[2][1]?.body).toBe(vi.mocked(fetch).mock.calls[1][1]?.body)
    resolveProgress(new Response(null, { status: 204 }))
    await flushPromises()
  })
})
