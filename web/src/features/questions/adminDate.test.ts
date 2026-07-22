import { afterEach,describe,expect,it,vi } from 'vitest'
import { adminDateBoundary,canonicalRFC3339 } from './adminDate'

describe('admin date serialization',()=>{
  afterEach(()=>vi.useRealTimers())
  it('serializes a historical local day with canonical start and end strings',()=>{const start=new Date(2025,6,21);const end=new Date(2025,6,21,23,59,59,999);expect(adminDateBoundary('2025-07-21','from',new Date(2025,6,22))).toBe(canonicalRFC3339(start));expect(adminDateBoundary('2025-07-21','to',new Date(2025,6,22))).toBe(canonicalRFC3339(end));expect(canonicalRFC3339(start)).not.toContain('.000Z')})
  it('caps today at now and emits the shortest canonical fractional seconds',()=>{vi.useFakeTimers();const now=new Date('2025-07-22T04:34:56.120Z');vi.setSystemTime(now);const value=adminDateBoundary('2025-07-22','to');expect(value).toBe('2025-07-22T04:34:56.12Z');expect(new Date(value!).getTime()).toBeLessThanOrEqual(Date.now());expect(canonicalRFC3339(new Date('2025-07-22T04:34:56.100Z'))).toBe('2025-07-22T04:34:56.1Z')})
})
