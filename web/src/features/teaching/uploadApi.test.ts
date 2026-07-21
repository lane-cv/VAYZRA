import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { browserUploadTransport } from './uploadApi'

function response(data: unknown, status = 200) { return new Response(JSON.stringify({ data }), { status }) }

describe('browserUploadTransport', () => {
  beforeEach(() => { document.cookie = 'hl_csrf=csrf-value; path=/'; vi.stubGlobal('fetch', vi.fn()) })
  afterEach(() => vi.unstubAllGlobals())

  it('sends binary parts with integrity and CSRF headers', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(response({ number: 2, size: 3, sha256: 'abc' }))
    const blob = new Blob([new Uint8Array([1, 2, 3])])
    await browserUploadTransport.putPart('session id', 2, blob, 'abc')
    const call = vi.mocked(fetch).mock.calls[0]
    const headers = new Headers(call[1]?.headers)
    expect(call[0]).toBe('/api/v1/admin/uploads/session%20id/parts/2')
    expect(call[1]?.method).toBe('PUT')
    expect(call[1]?.body).toBe(blob)
    expect(headers.get('Content-Type')).toBe('application/octet-stream')
    expect(headers.get('X-Part-SHA256')).toBe('abc')
    expect(headers.get('X-CSRF-Token')).toBe('csrf-value')
  })
})
