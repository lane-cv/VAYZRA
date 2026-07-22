import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const upload = vi.hoisted(() => ({ start: vi.fn(), onState: undefined as undefined | ((state: unknown) => void) }))
vi.mock('../teaching/uploadManager', async (original) => {
  const actual = await original<typeof import('../teaching/uploadManager')>()
  return { ...actual, createUploadManager: vi.fn((options: { onState: (state: unknown) => void }) => { upload.onState = options.onState; return { start: upload.start, cancel: vi.fn(), pause: vi.fn(), resume: vi.fn() } }) }
})
vi.mock('./studentApi', async (original) => ({ ...(await original<typeof import('./studentApi')>()), questionFileStatus: vi.fn().mockResolvedValue({ fileVersionId: 'v1', processingState: 'ready', detectedMime: 'application/pdf', size: 4, previewAvailable: true }) }))
import QuestionAttachmentUploader from './QuestionAttachmentUploader.vue'

describe('QuestionAttachmentUploader', () => {
  beforeEach(() => vi.clearAllMocks())
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
    expect(wrapper.emitted('update:attachments')?.slice(-1)[0]?.[0]).toEqual([{ fileVersionId: 'v1', sortPosition: 0 }])
  })
})
