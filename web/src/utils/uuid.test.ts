import { describe, expect, it, vi } from 'vitest'
import { uuidV4 } from './uuid'

describe('uuidV4', () => {
  it('uses native randomUUID when it is available', () => {
    const randomUUID = vi.fn(() => '11111111-1111-4111-8111-111111111111')
    expect(uuidV4({ randomUUID, getRandomValues: vi.fn() })).toBe('11111111-1111-4111-8111-111111111111')
    expect(randomUUID).toHaveBeenCalledOnce()
  })

  it('creates an RFC 4122 v4 UUID when randomUUID is unavailable', () => {
    const getRandomValues = vi.fn((target: Uint8Array) => { target.set(Array.from({ length: 16 }, (_, index) => index)); return target })
    expect(uuidV4({ getRandomValues })).toBe('00010203-0405-4607-8809-0a0b0c0d0e0f')
    expect(getRandomValues).toHaveBeenCalledOnce()
  })
})
