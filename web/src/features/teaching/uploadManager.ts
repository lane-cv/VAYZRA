export const UPLOAD_PART_SIZE = 8 * 1024 * 1024
const HASH_CHUNK_SIZE = 1024 * 1024

export interface UploadFile {
  readonly name: string
  readonly size: number
  readonly type: string
  readonly lastModified: number
  slice(start?: number, end?: number): Blob
}

export type UploadPart = { number: number; size: number; sha256: string }
export type UploadSession = {
  id: string; displayName: string; declaredMime: string; expectedSize: number; expectedSha256: string
  state: 'open' | 'completing' | 'completed' | 'cancelled' | 'expired'; expiresAt: string; parts: UploadPart[]
}
export type CompletedUpload = { fileId: string; fileVersionId: string; processingState: string }
export interface UploadTransport {
  create(input: { displayName: string; declaredMime: string; expectedSize: number; expectedSha256: string }): Promise<UploadSession>
  status(id: string): Promise<UploadSession>
  putPart(id: string, number: number, body: Blob, sha256: string, signal?: AbortSignal): Promise<UploadPart>
  complete(id: string): Promise<CompletedUpload>
  cancel(id: string): Promise<void>
}
export interface UploadSessionStore {
  get(fingerprint: string): Promise<string | undefined>
  set(fingerprint: string, sessionId: string): Promise<void>
  delete(fingerprint: string): Promise<void>
}
export type UploadManagerState =
  | { kind: 'idle' }
  | { kind: 'hashing'; progress: number }
  | { kind: 'uploading'; progress: number }
  | { kind: 'paused'; progress: number }
  | { kind: 'processing'; result: CompletedUpload }
  | { kind: 'cancelled' }
  | { kind: 'failed'; message: string }

type ManagerOptions = {
  transport: UploadTransport
  sessions: UploadSessionStore
  sleep?: (milliseconds: number) => Promise<void>
  random?: () => number
  onState?: (state: UploadManagerState) => void
}

export function uploadFingerprint(file: UploadFile) { return `${file.name}:${file.size}:${file.lastModified}` }

export function createUploadManager(options: ManagerOptions) {
  const sleep = options.sleep ?? ((milliseconds: number) => new Promise<void>((resolve) => setTimeout(resolve, milliseconds)))
  const random = options.random ?? Math.random
  type Operation = { file: UploadFile; fingerprint: string; sessionID: string; paused: boolean; cancelled: boolean; completing: boolean; uploadedBytes: number; controllers: Set<AbortController>; cleanup?: Promise<void> }
  let current: Operation | undefined
  let currentRun: Promise<CompletedUpload | undefined> | undefined
  const setState = (state: UploadManagerState) => options.onState?.(state)

  function start(file: UploadFile): Promise<CompletedUpload | undefined> {
    if (currentRun) return currentRun
    return launch(file)
  }

  function launch(file: UploadFile) {
    const operation: Operation = { file, fingerprint: uploadFingerprint(file), sessionID: '', paused: false, cancelled: false, completing: false, uploadedBytes: 0, controllers: new Set() }
    current = operation
    const running = run(operation)
    currentRun = running
    running.then(() => { if (currentRun === running) currentRun = undefined }, () => { if (currentRun === running) currentRun = undefined })
    return running
  }

  async function run(operation: Operation): Promise<CompletedUpload | undefined> {
    const file = operation.file
    setState({ kind: 'hashing', progress: 0 })
    const expectedSHA256 = await sha256File(file, HASH_CHUNK_SIZE, (progress) => {
      if (isActive(operation)) setState({ kind: 'hashing', progress })
    })
    if (shouldStop(operation)) { if (operation.cancelled) await cleanup(operation); return undefined }

    let session: UploadSession | undefined
    const savedID = await options.sessions.get(operation.fingerprint)
    if (savedID) {
      operation.sessionID = savedID
      if (shouldStop(operation)) {
        if (operation.cancelled) { await cleanup(operation); await options.sessions.delete(operation.fingerprint) }
        return undefined
      }
      try {
        const candidate = await options.transport.status(savedID)
        if ((candidate.state === 'open' || candidate.state === 'completing' || candidate.state === 'completed') && candidate.expectedSize === file.size && (!candidate.expectedSha256 || candidate.expectedSha256 === expectedSHA256)) session = candidate
        else { await options.sessions.delete(operation.fingerprint); operation.sessionID = '' }
      } catch {
        if (operation.cancelled) { await cleanup(operation); return undefined }
        await options.sessions.delete(operation.fingerprint)
        operation.sessionID = ''
      }
    }
    if (shouldStop(operation)) { if (operation.cancelled) await cleanup(operation); return undefined }
    if (!session) {
      session = await options.transport.create({ displayName: file.name, declaredMime: file.type || 'application/octet-stream', expectedSize: file.size, expectedSha256: expectedSHA256 })
      operation.sessionID = session.id
      await options.sessions.set(operation.fingerprint, session.id)
      if (shouldStop(operation)) {
        if (operation.cancelled) { await cleanup(operation); await options.sessions.delete(operation.fingerprint) }
        return undefined
      }
    }
    operation.sessionID = session.id
    const completedNumbers = new Set(session.parts.map((part) => part.number))
    operation.uploadedBytes = session.parts.reduce((sum, part) => sum + part.size, 0)
    if (isActive(operation)) setState({ kind: 'uploading', progress: progressOf(operation.uploadedBytes, file.size) })
    await uploadParts(operation, session.id, completedNumbers)
    if (shouldStop(operation)) { if (operation.cancelled) await cleanup(operation); return undefined }
    operation.completing = true
    const result = await options.transport.complete(session.id)
    await options.sessions.delete(operation.fingerprint)
    operation.sessionID = ''
    if (isCurrent(operation) && !operation.cancelled) setState({ kind: 'processing', result })
    return result
  }

  async function uploadParts(operation: Operation, sessionID: string, completed: Set<number>) {
    const file = operation.file
    const totalParts = Math.ceil(file.size / UPLOAD_PART_SIZE)
    const queue = Array.from({ length: totalParts }, (_, index) => index + 1).filter((number) => !completed.has(number))
    async function worker() {
      while (queue.length && isActive(operation)) {
        const number = queue.shift()
        if (!number) return
        const startOffset = (number - 1) * UPLOAD_PART_SIZE
        const body = file.slice(startOffset, Math.min(startOffset + UPLOAD_PART_SIZE, file.size))
        const sha256 = await sha256Blob(body)
        if (!isActive(operation)) return
        await putWithRetry(operation, sessionID, number, body, sha256)
        if (!isActive(operation)) return
        operation.uploadedBytes += body.size
        setState({ kind: 'uploading', progress: progressOf(operation.uploadedBytes, file.size) })
      }
    }
    try { await Promise.all([worker(), worker()]) }
    catch (error) {
      if (shouldStop(operation)) return
      const message = error instanceof Error ? error.message : '上传失败'
      if (isCurrent(operation)) setState({ kind: 'failed', message })
      throw error
    }
  }

  async function putWithRetry(operation: Operation, sessionID: string, number: number, body: Blob, sha256: string) {
    for (let attempt = 0; attempt < 3; attempt += 1) {
      if (!isActive(operation)) return undefined
      const controller = new AbortController()
      operation.controllers.add(controller)
      try { return await options.transport.putPart(sessionID, number, body, sha256, controller.signal) }
      catch (error) {
        if (!isActive(operation)) return undefined
        if (attempt === 2) throw error
        await sleep(Math.min(3000, 250 * (2 ** attempt) + Math.floor(random() * 200)))
      } finally { operation.controllers.delete(controller) }
    }
  }

  function pause() {
    if (!current || current.cancelled || current.completing) return
    current.paused = true
    for (const controller of current.controllers) controller.abort()
    setState({ kind: 'paused', progress: progressOf(current.uploadedBytes, current.file.size) })
  }
  async function resume() {
    const operation = current
    if (!operation?.paused || operation.cancelled) return undefined
    const previous = currentRun
    if (previous) await previous.catch(() => undefined)
    if (current !== operation || operation.cancelled) return undefined
    return launch(operation.file)
  }
  async function cancel() {
    const operation = current
    if (!operation || operation.completing) return
    operation.cancelled = true
    operation.paused = false
    for (const controller of operation.controllers) controller.abort()
    await cleanup(operation)
    if (isCurrent(operation)) setState({ kind: 'cancelled' })
  }
  function isCurrent(operation: Operation) { return current === operation }
  function isActive(operation: Operation) { return isCurrent(operation) && !operation.paused && !operation.cancelled }
  function shouldStop(operation: Operation) { return !isActive(operation) }
  async function cleanup(operation: Operation) {
    if (!operation.sessionID) return
    if (!operation.cleanup) operation.cleanup = (async () => {
      await options.transport.cancel(operation.sessionID)
      await options.sessions.delete(operation.fingerprint)
      operation.sessionID = ''
    })()
    await operation.cleanup
  }
  return { start, pause, resume, cancel }
}

function progressOf(done: number, total: number) { return total ? Math.min(100, Math.round((done / total) * 100)) : 100 }

export async function sha256File(file: Pick<UploadFile, 'size' | 'slice'>, chunkSize = HASH_CHUNK_SIZE, onProgress?: (progress: number) => void) {
  const hash = new SHA256()
  for (let offset = 0; offset < file.size; offset += chunkSize) {
    const bytes = new Uint8Array(await blobArrayBuffer(file.slice(offset, Math.min(offset + chunkSize, file.size))))
    hash.update(bytes)
    onProgress?.(progressOf(Math.min(offset + chunkSize, file.size), file.size))
    await Promise.resolve()
  }
  return hash.hex()
}

async function sha256Blob(blob: Blob) {
  const hash = new SHA256()
  const chunk = 256 * 1024
  for (let offset = 0; offset < blob.size; offset += chunk) hash.update(new Uint8Array(await blobArrayBuffer(blob.slice(offset, Math.min(offset + chunk, blob.size)))))
  return hash.hex()
}

function blobArrayBuffer(blob: Blob): Promise<ArrayBuffer> {
  if (typeof blob.arrayBuffer === 'function') return blob.arrayBuffer()
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as ArrayBuffer)
    reader.onerror = () => reject(reader.error ?? new Error('读取文件失败'))
    reader.readAsArrayBuffer(blob)
  })
}

const K = new Uint32Array([
  0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
  0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
  0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
  0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2,
])

class SHA256 {
  private state = new Uint32Array([0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19])
  private buffer = new Uint8Array(64)
  private bufferLength = 0
  private bytesHashed = 0
  update(bytes: Uint8Array) {
    this.bytesHashed += bytes.length
    let offset = 0
    while (offset < bytes.length) {
      const take = Math.min(64 - this.bufferLength, bytes.length - offset)
      this.buffer.set(bytes.subarray(offset, offset + take), this.bufferLength)
      this.bufferLength += take
      offset += take
      if (this.bufferLength === 64) { this.compress(this.buffer); this.bufferLength = 0 }
    }
  }
  hex() {
    const bitLength = this.bytesHashed * 8
    const paddingLength = this.bufferLength < 56 ? 64 - this.bufferLength : 128 - this.bufferLength
    const padding = new Uint8Array(paddingLength)
    padding[0] = 0x80
    const view = new DataView(padding.buffer)
    view.setUint32(paddingLength - 8, Math.floor(bitLength / 0x100000000), false)
    view.setUint32(paddingLength - 4, bitLength >>> 0, false)
    this.update(padding)
    return [...this.state].map((word) => word.toString(16).padStart(8, '0')).join('')
  }
  private compress(block: Uint8Array) {
    const words = new Uint32Array(64)
    const view = new DataView(block.buffer, block.byteOffset, block.byteLength)
    for (let index = 0; index < 16; index += 1) words[index] = view.getUint32(index * 4, false)
    for (let index = 16; index < 64; index += 1) {
      const a = words[index - 15], b = words[index - 2]
      const s0 = rotate(a, 7) ^ rotate(a, 18) ^ (a >>> 3)
      const s1 = rotate(b, 17) ^ rotate(b, 19) ^ (b >>> 10)
      words[index] = (words[index - 16] + s0 + words[index - 7] + s1) >>> 0
    }
    let [a,b,c,d,e,f,g,h] = this.state
    for (let index = 0; index < 64; index += 1) {
      const s1 = rotate(e, 6) ^ rotate(e, 11) ^ rotate(e, 25)
      const choice = (e & f) ^ (~e & g)
      const t1 = (h + s1 + choice + K[index] + words[index]) >>> 0
      const s0 = rotate(a, 2) ^ rotate(a, 13) ^ rotate(a, 22)
      const majority = (a & b) ^ (a & c) ^ (b & c)
      const t2 = (s0 + majority) >>> 0
      h=g; g=f; f=e; e=(d+t1)>>>0; d=c; c=b; b=a; a=(t1+t2)>>>0
    }
    this.state[0]=(this.state[0]+a)>>>0; this.state[1]=(this.state[1]+b)>>>0; this.state[2]=(this.state[2]+c)>>>0; this.state[3]=(this.state[3]+d)>>>0
    this.state[4]=(this.state[4]+e)>>>0; this.state[5]=(this.state[5]+f)>>>0; this.state[6]=(this.state[6]+g)>>>0; this.state[7]=(this.state[7]+h)>>>0
  }
}
function rotate(value: number, bits: number) { return (value >>> bits) | (value << (32 - bits)) }

export function createIndexedDBUploadSessionStore(): UploadSessionStore {
  const database = () => new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open('vayzra-upload-sessions', 1)
    request.onupgradeneeded = () => request.result.createObjectStore('sessions')
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('打开上传会话数据库失败'))
  })
  const access = async <T>(mode: IDBTransactionMode, action: (store: IDBObjectStore) => IDBRequest<T>) => {
    const db = await database()
    return new Promise<T>((resolve, reject) => {
      const transaction = db.transaction('sessions', mode)
      const request = action(transaction.objectStore('sessions'))
      request.onsuccess = () => resolve(request.result)
      request.onerror = () => reject(request.error ?? new Error('访问上传会话数据库失败'))
      transaction.oncomplete = () => db.close()
    })
  }
  return {
    get: async (key) => access<string | undefined>('readonly', (store) => store.get(key) as IDBRequest<string | undefined>),
    set: async (key, value) => { await access<IDBValidKey>('readwrite', (store) => store.put(value, key)) },
    delete: async (key) => { await access<undefined>('readwrite', (store) => store.delete(key)) },
  }
}
