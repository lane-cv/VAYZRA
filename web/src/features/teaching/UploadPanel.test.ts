import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

const manager = vi.hoisted(() => ({ start: vi.fn(), pause: vi.fn(), resume: vi.fn(), cancel: vi.fn() }))
const bindingAPI = vi.hoisted(() => ({ fileDetail: vi.fn(), listLessonFiles: vi.fn(), replaceLessonFiles: vi.fn() }))
let notifyState: ((state: unknown) => void) | undefined
vi.mock('./uploadManager', async (original) => {
  const actual = await original<typeof import('./uploadManager')>()
  return { ...actual, createIndexedDBUploadSessionStore: vi.fn(() => ({})), createUploadManager: vi.fn((options: { onState: (state: unknown) => void }) => { notifyState = options.onState; return manager }) }
})
vi.mock('../files/api', () => ({ fileDetail: bindingAPI.fileDetail }))
vi.mock('./api', () => ({ listLessonFiles: bindingAPI.listLessonFiles, replaceLessonFiles: bindingAPI.replaceLessonFiles }))

import UploadPanel from './UploadPanel.vue'

describe('UploadPanel', () => {
  afterEach(() => vi.clearAllMocks())

  it('shows hashing/upload/processing states and exposes pause, resume, and cancel', async () => {
    manager.start.mockResolvedValue(undefined)
    const wrapper = mount(UploadPanel)
    await wrapper.get('input[value="preview"]').setValue(true)
    const file = new File(['lesson'], 'lesson.pdf', { type: 'application/pdf', lastModified: 123 })
    Object.defineProperty(wrapper.get('input[type="file"]').element, 'files', { value: [file] })
    await wrapper.get('input[type="file"]').trigger('change')
    expect(manager.start).toHaveBeenCalledWith(file)

    notifyState?.({ kind: 'hashing', progress: 40 }); await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('正在校验文件 40%')
    notifyState?.({ kind: 'uploading', progress: 55 }); await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('正在上传 55%')
    await wrapper.get('button[aria-label="暂停上传"]').trigger('click')
    expect(manager.pause).toHaveBeenCalled()
    notifyState?.({ kind: 'paused', progress: 55 }); await wrapper.vm.$nextTick()
    await wrapper.get('button[aria-label="继续上传"]').trigger('click')
    expect(manager.resume).toHaveBeenCalled()
    await wrapper.get('button[aria-label="取消上传"]').trigger('click')
    expect(manager.cancel).toHaveBeenCalled()
    notifyState?.({ kind: 'processing', result: { fileId: 'f1', fileVersionId: 'v1', processingState: 'pending' } }); await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('上传完成，正在进行安全扫描和预览转换')
  })

  it('requires an explicit preview or download policy before selecting a file', () => {
    const wrapper = mount(UploadPanel)
    expect(wrapper.get('input[type="file"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('请先选择学生访问方式')
  })

  it('checks processing readiness and merges the upload into existing lesson bindings', async () => {
    bindingAPI.fileDetail.mockResolvedValue({ id: 'f1', createdAt: '', versions: [{ id: 'v1', fileId: 'f1', version: 1, displayName: 'lesson.pdf', declaredMime: 'application/pdf', size: 10, processingState: 'ready', previewState: 'ready', browserPlayable: false, createdAt: '' }], references: [] })
    bindingAPI.listLessonFiles.mockResolvedValue([{ id: 'b1', lessonId: 'lesson-1', fileVersionId: 'old-v', policy: 'download', displayName: 'old.pdf', description: '', sortPosition: 10 }])
    bindingAPI.replaceLessonFiles.mockResolvedValue([])
    const wrapper = mount(UploadPanel, { props: { lessonId: 'lesson-1', lockVersion: 7, canBind: true } })
    await wrapper.get('input[value="preview"]').setValue(true)
    notifyState?.({ kind: 'processing', result: { fileId: 'f1', fileVersionId: 'v1', processingState: 'pending_scan' } })
    await wrapper.vm.$nextTick()
    await wrapper.get('button[aria-label="检查文件处理状态"]').trigger('click')
    expect(wrapper.text()).toContain('文件已就绪，可以绑定')
    await wrapper.get('button[aria-label="绑定文件到课程"]').trigger('click')
    expect(bindingAPI.replaceLessonFiles).toHaveBeenCalledWith('lesson-1', 7, [
      { fileVersionId: 'old-v', policy: 'download', displayName: 'old.pdf', description: '', sortPosition: 10 },
      { fileVersionId: 'v1', policy: 'preview', displayName: 'lesson.pdf', description: '', sortPosition: 20 },
    ])
    expect(wrapper.emitted('bindingChanged')).toBeTruthy()
  })
})
