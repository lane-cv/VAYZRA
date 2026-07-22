import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
const backing = vi.hoisted(() => ({ get: vi.fn(), set: vi.fn(), delete: vi.fn() }))
vi.mock('../teaching/uploadManager', async (original) => ({ ...(await original<typeof import('../teaching/uploadManager')>()), createIndexedDBUploadSessionStore: vi.fn(() => backing) }))
import { createStudentQuestionSessionStore, studentQuestionUploadTransport } from './questionUpload'

describe('student question upload adapters', () => {
  beforeEach(() => { vi.clearAllMocks(); document.cookie = 'hl_csrf=csrf; path=/'; vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: { id: 'upload-1', parts: [] } })))) })
  afterEach(() => vi.unstubAllGlobals())
  it('isolates resume keys by student and feature namespace', async () => {
    const store = createStudentQuestionSessionStore('user:1')
    await store.get('file:4:2'); await store.set('file:4:2', 'upload-1'); await store.delete('file:4:2')
    expect(backing.get).toHaveBeenCalledWith('qa:student:user:1:file:4:2')
    expect(backing.set).toHaveBeenCalledWith('qa:student:user:1:file:4:2', 'upload-1')
    expect(backing.delete).toHaveBeenCalledWith('qa:student:user:1:file:4:2')
  })
  it('uses only the student question upload prefix', async () => {
    await studentQuestionUploadTransport.create({ displayName: 'x.pdf', declaredMime: 'application/pdf', expectedSize: 1, expectedSha256: 'a'.repeat(64) })
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/student/question-uploads')
  })
})
