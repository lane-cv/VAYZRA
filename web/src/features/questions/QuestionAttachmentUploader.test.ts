import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const upload = vi.hoisted(() => ({ start: vi.fn(), cancel: vi.fn(), onState: undefined as undefined | ((state: unknown) => void) }))
const api = vi.hoisted(() => ({ status: vi.fn() }))
vi.mock('../teaching/uploadManager', async (original) => {
  const actual = await original<typeof import('../teaching/uploadManager')>()
  return { ...actual, createUploadManager: vi.fn((options: { onState: (state: unknown) => void }) => { upload.onState = options.onState; return { start: upload.start, cancel: upload.cancel, pause: vi.fn(), resume: vi.fn() } }) }
})
vi.mock('./studentApi', async (original) => ({ ...(await original<typeof import('./studentApi')>()), questionFileStatus: api.status }))
import QuestionAttachmentUploader from './QuestionAttachmentUploader.vue'

describe('QuestionAttachmentUploader', () => {
  beforeEach(() => { vi.clearAllMocks(); api.status.mockResolvedValue({ fileVersionId: 'v1', processingState: 'ready', detectedMime: 'application/pdf', size: 4, previewAvailable: true }) })
  it('rejects unsupported/oversized aggregate selections before upload', async () => {
    const wrapper = mount(QuestionAttachmentUploader, { props: { userId: 'u1' } })
    const input = wrapper.get('input[type=file]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['x'], 'bad.exe', { type: 'application/x-msdownload' })] })
    await input.trigger('change')
    expect(wrapper.get('[role=alert]').text()).toContain('不支持')
    expect(upload.start).not.toHaveBeenCalled()
  })
  it('emits only server-confirmed ready versions and reports pending progress', async () => {
    upload.start.mockImplementation(async () => { upload.onState?.({ kind: 'uploading', progress: 40 }); return { fileId: 'f1', fileVersionId: 'v1', processingState: 'pending_scan' } })
    const wrapper = mount(QuestionAttachmentUploader, { props: { userId: 'u1' } })
    const file = new File(['data'], 'answer.pdf', { type: 'application/pdf' })
    Object.defineProperty(wrapper.get('input').element, 'files', { configurable: true, value: [file] })
    await wrapper.get('input').trigger('change'); await flushPromises()
    expect(wrapper.text()).toContain('answer.pdf')
    expect(wrapper.text()).toContain('已就绪')
    expect(wrapper.emitted('update:attachments')?.slice(-1)[0]?.[0]).toEqual([{ fileVersionId: 'v1', sortPosition: 0 }])
  })
  it('cancels hashing and stops the remaining file queue when unmounted', async () => {
    let resolveStart!: (value: unknown) => void
    upload.start.mockImplementationOnce(() => new Promise((resolve) => { resolveStart = resolve }))
    const wrapper = mount(QuestionAttachmentUploader, { props: { userId: 'u1' } })
    const files = [new File(['a'], 'one.pdf', { type: 'application/pdf' }), new File(['b'], 'two.pdf', { type: 'application/pdf' })]
    Object.defineProperty(wrapper.get('input').element, 'files', { configurable: true, value: files }); await wrapper.get('input').trigger('change')
    wrapper.unmount(); expect(upload.cancel).toHaveBeenCalledTimes(1)
    resolveStart({ fileId: 'f1', fileVersionId: 'v1', processingState: 'pending_scan' }); await flushPromises()
    expect(upload.start).toHaveBeenCalledTimes(1); expect(api.status).not.toHaveBeenCalled()
  })
  it('aborts polling and never emits a late ready result after identity changes', async () => {
    upload.start.mockResolvedValue({ fileId: 'f1', fileVersionId: 'v1', processingState: 'pending_scan' })
    let resolveStatus!: (value: unknown) => void
    api.status.mockImplementationOnce((_id: string, signal: AbortSignal) => new Promise((resolve, reject) => { resolveStatus = resolve; signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))) }))
    const wrapper = mount(QuestionAttachmentUploader, { props: { userId: 'u1' } })
    Object.defineProperty(wrapper.get('input').element, 'files', { configurable: true, value: [new File(['a'], 'one.pdf', { type: 'application/pdf' })] }); await wrapper.get('input').trigger('change'); await flushPromises()
    const emissionsBefore = wrapper.emitted('update:attachments')?.length ?? 0
    await wrapper.setProps({ userId: 'u2' }); await flushPromises(); resolveStatus({ fileVersionId: 'v1', processingState: 'ready', size: 1, previewAvailable: true }); await flushPromises()
    const later = wrapper.emitted('update:attachments')?.slice(emissionsBefore) ?? []
    expect(later.some((event) => Array.isArray(event[0]) && event[0].some((item: { fileVersionId: string }) => item.fileVersionId === 'v1'))).toBe(false)
    expect(api.status).toHaveBeenCalledTimes(1)
  })
})
