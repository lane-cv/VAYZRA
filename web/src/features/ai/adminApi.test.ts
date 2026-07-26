import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ProviderView } from './adminApi'
import {
  activateProvider,
  createProvider,
  listProviders,
  listUsageRuns,
  readUsageSummary,
  testProvider,
  updateProvider,
  putModel,
  putPrompt,
  putGlobalLimits,
  putStudentLimits,
} from './adminApi'

type ForbiddenProviderReadKey = Extract<
  keyof ProviderView,
  'apiKey' | 'encryptedApiKey' | 'nonce' | 'fingerprint'
>
const providerReadHasNoSecretKey: ForbiddenProviderReadKey extends never ? true : false = true

const limits = {
  dailyRequests: { mode: 'disabled' as const },
  monthlyRequests: { mode: 'disabled' as const },
  dailyTokens: { mode: 'disabled' as const },
  monthlyTokens: { mode: 'disabled' as const },
  expectedVersion: 4,
}

describe('admin AI api', () => {
  beforeEach(() => {
    document.cookie = 'hl_csrf=csrf; path=/'
    vi.stubGlobal('fetch', vi.fn().mockImplementation(
      async () => new Response(JSON.stringify({ data: {} })),
    ))
  })

  afterEach(() => vi.unstubAllGlobals())

  it('keeps provider read DTOs secret-free while exposing hasKey', async () => {
    expect(providerReadHasNoSecretKey).toBe(true)
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: [{
        id: 'p1',
        name: 'Provider',
        baseUrl: 'https://provider.example',
        protocolMode: 'responses',
        active: true,
        hasKey: true,
        keyUpdatedAt: '2026-07-27T00:00:00Z',
        version: 2,
      }],
    })))
    const result = await listProviders()
    expect(result[0]).toEqual(expect.objectContaining({ hasKey: true }))
    expect(Object.keys(result[0])).not.toEqual(expect.arrayContaining([
      'apiKey',
      'encryptedApiKey',
      'nonce',
      'fingerprint',
    ]))
  })

  it('uses separate strict provider write inputs for create, update, activate, and test', async () => {
    vi.mocked(fetch).mockImplementation(async (input) => String(input).endsWith('/test')
      ? new Response(JSON.stringify({
          ok: true,
          protocol: 'responses',
          latencyMs: 15,
          errorCategory: '',
        }))
      : new Response(JSON.stringify({ data: {} })))
    await createProvider({
      name: 'P',
      baseUrl: 'https://p.test',
      protocolMode: 'responses',
      apiKey: 'secret',
      expectedVersion: 99,
    }, '12345678-1234-4234-8234-123456789012')
    await updateProvider('provider /?', {
      name: 'P2',
      baseUrl: 'https://p2.test',
      protocolMode: 'chat_completions',
      expectedVersion: 3,
    })
    await activateProvider('provider-id', 4)
    await testProvider('provider /?')

    const calls = vi.mocked(fetch).mock.calls
    expect(calls.map(([url]) => url)).toEqual([
      '/api/v1/admin/ai/providers',
      '/api/v1/admin/ai/providers/provider%20%2F%3F',
      '/api/v1/admin/ai/active-provider',
      '/api/v1/admin/ai/providers/provider%20%2F%3F/test',
    ])
    expect(calls.map(([, init]) => init?.method)).toEqual(['POST', 'PUT', 'PUT', 'POST'])
    expect(calls.slice(0, 3).map(([, init]) => JSON.parse(String(init?.body)))).toEqual([
      { name: 'P', baseUrl: 'https://p.test', protocolMode: 'responses', apiKey: 'secret' },
      { name: 'P2', baseUrl: 'https://p2.test', protocolMode: 'chat_completions', expectedVersion: 3 },
      { providerId: 'provider-id', expectedVersion: 4 },
    ])
    expect(calls[3][1]?.body).toBeUndefined()
  })

  it('sends exact model, prompt, global-limit, and student-limit writes', async () => {
    await putModel('provider /?', 'model /?', {
      upstreamModelId: 'gpt-x',
      modality: 'vision',
      contextTokens: 100,
      maxOutputTokens: 20,
      imageQuotaTokens: 4,
      inputPriceMicroUsd: 9,
      outputPriceMicroUsd: 12,
      enabled: true,
      clearQuotaBlock: false,
      expectedVersion: 2,
    })
    await putPrompt('physics', { body: 'prompt', expectedVersion: 5 })
    await putGlobalLimits(limits)
    await putStudentLimits('student /?', limits)

    const calls = vi.mocked(fetch).mock.calls
    expect(calls.map(([url]) => url)).toEqual([
      '/api/v1/admin/ai/providers/provider%20%2F%3F/models/model%20%2F%3F',
      '/api/v1/admin/ai/prompts/physics',
      '/api/v1/admin/ai/limits/global',
      '/api/v1/admin/ai/limits/students/student%20%2F%3F',
    ])
    expect(calls.map(([, init]) => JSON.parse(String(init?.body)))).toEqual([
      {
        upstreamModelId: 'gpt-x',
        modality: 'vision',
        contextTokens: 100,
        maxOutputTokens: 20,
        imageQuotaTokens: 4,
        inputPriceMicroUsd: 9,
        outputPriceMicroUsd: 12,
        enabled: true,
        clearQuotaBlock: false,
        expectedVersion: 2,
      },
      { body: 'prompt', expectedVersion: 5 },
      limits,
      limits,
    ])
  })

  it('strictly encodes usage filters and keeps micro-USD cost as a string', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { costMicroUSD: '9007199254740993' } })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [{ id: 'r1', costMicroUSD: '9007199254740993' }],
        meta: { nextCursor: 'next' },
      })))

    const filters = {
      studentId: 'student /?',
      modelId: 'model /?',
      status: 'failed' as const,
      from: '2026-07-01T00:00:00Z',
      to: '2026-07-27T00:00:00Z',
      cursor: 'a+b/=',
      limit: 25,
    }
    await expect(readUsageSummary(filters)).resolves.toMatchObject({ costMicroUSD: '9007199254740993' })
    await expect(listUsageRuns(filters)).resolves.toMatchObject({
      items: [{ costMicroUSD: '9007199254740993' }],
      nextCursor: 'next',
    })
    expect(vi.mocked(fetch).mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/admin/ai/usage/summary?studentId=student+%2F%3F&modelId=model+%2F%3F&status=failed&from=2026-07-01T00%3A00%3A00Z&to=2026-07-27T00%3A00%3A00Z&cursor=a%2Bb%2F%3D&limit=25',
      '/api/v1/admin/ai/usage/runs?studentId=student+%2F%3F&modelId=model+%2F%3F&status=failed&from=2026-07-01T00%3A00%3A00Z&to=2026-07-27T00%3A00%3A00Z&cursor=a%2Bb%2F%3D&limit=25',
    ])
  })

  it('preserves structured errors from provider connectivity requests', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      error: { code: 'rate_limited', message: 'slow down', requestId: 'req-test' },
    }), { status: 429 }))

    await expect(testProvider('provider-id')).rejects.toMatchObject({
      status: 429,
      code: 'rate_limited',
      requestId: 'req-test',
    })
  })

  it.each([
    {
      status: 409,
      category: 'busy',
      requestId: 'req-busy',
    },
    {
      status: 503,
      category: 'auth',
      requestId: 'req-unavailable',
    },
  ])('rejects raw provider test failure status $status using only normalized headers', async ({
    status,
    category,
    requestId,
  }) => {
    const secret = 'raw-provider-secret'
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      ok: false,
      protocol: 'responses',
      latencyMs: 12,
      errorCategory: category,
      apiKey: secret,
      rawDetail: secret,
    }), {
      status,
      headers: {
        'X-Error-Code': ' provider_unavailable ',
        'X-Request-ID': requestId,
      },
    }))

    const error = await testProvider('provider-id').catch((reason: unknown) => reason)

    expect(error).toMatchObject({
      status,
      code: 'PROVIDER_UNAVAILABLE',
      requestId,
    })
    expect(String(error)).not.toContain(secret)
    expect(JSON.stringify(error)).not.toContain(secret)
  })

  it('checks failure status and allowlisted headers before attempting JSON parsing', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response('not-json', {
      status: 503,
      headers: {
        'X-Error-Code': 'PROVIDER_UNAVAILABLE',
        'X-Request-ID': 'req-header',
      },
    }))

    await expect(testProvider('provider-id')).rejects.toMatchObject({
      status: 503,
      code: 'PROVIDER_UNAVAILABLE',
      requestId: 'req-header',
    })
  })

  it('projects a valid success into an exact redacted connectivity DTO', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      ok: true,
      protocol: 'chat_completions',
      latencyMs: 17,
      errorCategory: '',
    }), { status: 200 }))

    await expect(testProvider('provider-id')).resolves.toStrictEqual({
      ok: true,
      protocol: 'chat_completions',
      latencyMs: 17,
      errorCategory: '',
    })
  })

  it('rejects successful connectivity payloads with extra secret-bearing fields', async () => {
    const secret = 'must-not-survive'
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      ok: true,
      protocol: 'responses',
      latencyMs: 9,
      errorCategory: '',
      encryptedApiKey: secret,
      nonce: secret,
    }), { status: 200 }))

    const error = await testProvider('provider-id').catch((reason: unknown) => reason)
    expect(error).toMatchObject({ status: 200, code: 'invalid_response' })
    expect(JSON.stringify(error)).not.toContain(secret)
  })

  it.each([
    ['unknown protocol', { ok: true, protocol: 'legacy', latencyMs: 1, errorCategory: '' }],
    ['fractional latency', { ok: true, protocol: 'responses', latencyMs: 1.5, errorCategory: '' }],
    ['negative latency', { ok: true, protocol: 'responses', latencyMs: -1, errorCategory: '' }],
    ['unknown category', { ok: true, protocol: 'responses', latencyMs: 1, errorCategory: 'raw-upstream' }],
    ['failure on 2xx', { ok: false, protocol: 'responses', latencyMs: 1, errorCategory: 'auth' }],
    ['missing field', { ok: true, protocol: 'responses', latencyMs: 1 }],
  ])('rejects invalid successful connectivity payload: %s', async (_name, payload) => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify(payload), { status: 200 }))

    await expect(testProvider('provider-id')).rejects.toMatchObject({
      status: 200,
      code: 'invalid_response',
    })
  })

  it('bounds successful raw JSON and rejects an oversized response', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      ok: true,
      protocol: 'responses',
      latencyMs: 1,
      errorCategory: '',
      padding: 'x'.repeat(20_000),
    }), { status: 200 }))

    await expect(testProvider('provider-id')).rejects.toMatchObject({
      status: 200,
      code: 'invalid_response',
    })
  })

  it('honors an already-aborted connectivity request without fetching', async () => {
    const controller = new AbortController()
    controller.abort()

    await expect(testProvider('provider-id', controller.signal)).rejects.toMatchObject({
      name: 'AbortError',
    })
    expect(fetch).not.toHaveBeenCalled()
  })
})
