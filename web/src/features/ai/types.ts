import type { AttachmentInput, QuestionAttachment } from '../questions/types'

export type AIChannel = 'ai' | 'teacher'
export type AISubject = 'math' | 'physics'
export type AIRunStatus = 'queued' | 'streaming' | 'succeeded' | 'failed' | 'cancelled'

export type AIThread = {
  id: string
  title: string
  subject: AISubject
  lastMessageAt: string
  createdAt: string
}

export type AIMessage = {
  id: string
  role: 'student' | 'assistant'
  body: string
  createdAt: string
  attachments: QuestionAttachment[]
  runId?: string
}

export type AIUsage = {
  inputTokens: number
  outputTokens: number
  costMicroUSD: string
  source: 'provider' | 'estimated' | 'unknown'
}

export type AIRun = {
  id: string
  status: AIRunStatus
  attemptNo: number
  lastSequence: number
  errorCode?: string
  usage?: AIUsage
  createdAt?: string
  updatedAt?: string
}

export type AIThreadDetail = {
  thread: AIThread
  messages: AIMessage[]
  activeRun?: AIRun
  nextMessageCursor?: string
}

export type AIThreadPage = { items: AIThread[]; nextCursor?: string }
export type AIThreadMutation = {
  thread?: AIThread
  message?: AIMessage
  run: AIRun
  eventsUrl: string
}

export type CreateAIThreadInput = {
  title: string
  subject: AISubject
  body: string
  attachments: AttachmentInput[]
}

export type AddAIMessageInput = {
  body: string
  attachments: AttachmentInput[]
}

export type StreamEvent = {
  sequence: number
  kind: 'delta' | 'status' | 'error'
  delta?: string
  status?: AIRunStatus
  errorCode?: string
}

export type StreamCallbacks = {
  onEvent(event: StreamEvent): void
  onRequestId?(requestId: string): void
}

export type AIFileStatus = {
  fileVersionId: string
  processingState: string
  failureCategory?: string
  detectedMime?: string
  size: number
  previewAvailable: boolean
}
