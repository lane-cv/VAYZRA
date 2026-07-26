import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const backing = vi.hoisted(() => ({
  get: vi.fn(),
  set: vi.fn(),
  delete: vi.fn(),
  createUploadManager: vi.fn((options: unknown) => ({ options })),
}))

vi.mock('../teaching/uploadManager', async (original) => ({
  ...(await original<typeof import('../teaching/uploadManager')>()),
  createIndexedDBUploadSessionStore: vi.fn(() => backing),
  createUploadManager: backing.createUploadManager,
}))

import {
  aiFilePreviewURL,
  aiFileStatus,
  aiUploadTransport,
  createAIUploadManager,
  createAIUploadSessionStore,
} from './aiUpload'

describe('AI upload adapters', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    document.cookie = 'hl_csrf=csrf; path=/'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: { id: 'upload-1', parts: [] },
    }))))
  })

  afterEach(() => vi.unstubAllGlobals())

  it('isolates resumable state by AI namespace and user without storing content', async () => {
    const store = createAIUploadSessionStore('user:1')
    await store.get('file.pdf:4:2')
    await store.set('file.pdf:4:2', 'upload-1')
    await store.delete('file.pdf:4:2')

    expect(backing.get).toHaveBeenCalledWith('qa:ai:user:1:file.pdf:4:2')
    expect(backing.set).toHaveBeenCalledWith('qa:ai:user:1:file.pdf:4:2', 'upload-1')
    expect(backing.delete).toHaveBeenCalledWith('qa:ai:user:1:file.pdf:4:2')
  })

  it('reuses createUploadManager with the AI transport and namespaced store', () => {
    const onState = vi.fn()
    createAIUploadManager('user-1', onState)

    expect(backing.createUploadManager).toHaveBeenCalledOnce()
    expect(backing.createUploadManager).toHaveBeenCalledWith(expect.objectContaining({
      transport: aiUploadTransport,
      onState,
    }))
  })

  it('uses only the student AI upload prefix', async () => {
    await aiUploadTransport.create({
      displayName: 'x.pdf',
      declaredMime: 'application/pdf',
      expectedSize: 1,
      expectedSha256: 'a'.repeat(64),
    })

    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/student/ai-uploads')
  })

  it('uses purpose-specific status and same-origin preview URLs', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      fileVersionId: 'version /?',
      processingState: 'ready',
      detectedMime: 'application/pdf',
      size: 3,
      previewAvailable: true,
    })))

    await expect(aiFileStatus('version /?')).resolves.toMatchObject({
      processingState: 'ready',
      previewAvailable: true,
    })
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/ai-question-files/version%20%2F%3F/status')
    expect(aiFilePreviewURL('version /?')).toBe('/api/v1/ai-question-files/version%20%2F%3F/preview')
  })
})
