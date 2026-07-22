import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { APIError } from '../api/client'
import { listNotifications, markAllNotificationsRead, markNotificationRead, unreadNotificationCount } from '../features/notifications/api'
import type { NotificationItem } from '../features/notifications/types'

const POLL_MS = 15_000
const WAKE_COOLDOWN_MS = 250
export const useNotificationStore = defineStore('notifications', () => {
  const unreadCount = ref(0), activeUserId = ref<string>(), items = ref<NotificationItem[]>([]), nextCursor = ref<string>()
  const listLoading = ref(false), listError = ref(''), requestId = ref('')
  let timer: ReturnType<typeof setInterval> | undefined, countController: AbortController | undefined
  let countPromise: Promise<void> | undefined, listController: AbortController | undefined
  let mutationController: AbortController | undefined
  let generation = 0, listGeneration = 0, mutationGeneration = 0, lastWakeAt: number | undefined

  function clearTimer() { if (timer !== undefined) { clearInterval(timer); timer = undefined } }
  function schedule() { clearTimer(); if (activeUserId.value && !document.hidden) timer = setInterval(() => void refresh(), POLL_MS) }
  function wake() {
    if (!activeUserId.value || document.hidden) return
    const now = performance.now()
    if (lastWakeAt !== undefined && now - lastWakeAt < WAKE_COOLDOWN_MS) return
    lastWakeAt = now
    void refresh()
  }
  function visibility() { if (document.hidden) { clearTimer(); lastWakeAt = undefined } else { schedule(); wake() } }
  function focus() { wake() }

  async function refresh(): Promise<void> {
    if (!activeUserId.value || countPromise) return countPromise
    const current = generation, controller = new AbortController(); countController = controller
    const job = unreadNotificationCount(controller.signal).then((count) => { if (current === generation && !controller.signal.aborted) unreadCount.value = count }).catch((cause: unknown) => {
      if (controller.signal.aborted || current !== generation) return
      if (cause instanceof APIError && cause.status === 401) stop()
    }).finally(() => { if (countPromise === job) countPromise = undefined; if (countController === controller) countController = undefined })
    countPromise = job
    return job
  }
  function start(userId: string) {
    if (!userId || activeUserId.value === userId) return
    stop(); activeUserId.value = userId
    document.addEventListener('visibilitychange', visibility); window.addEventListener('focus', focus)
    schedule(); void refresh()
  }
  function cancelList() { listGeneration++; listController?.abort(); listController = undefined; listLoading.value = false }
  function cancelMutations() { mutationGeneration++; mutationController?.abort(); mutationController = undefined }
  function stop() {
    generation++; activeUserId.value = undefined; lastWakeAt = undefined; clearTimer()
    document.removeEventListener('visibilitychange', visibility); window.removeEventListener('focus', focus)
    countController?.abort(); countController = undefined; countPromise = undefined; cancelList(); cancelMutations()
    unreadCount.value = 0; items.value = []; nextCursor.value = undefined; listError.value = ''; requestId.value = ''
  }
  async function list(cursor?: string) {
    const current = ++listGeneration; listController?.abort(); const controller = new AbortController(); listController = controller
    listLoading.value = true; listError.value = ''; requestId.value = ''
    try {
      const page = await listNotifications(cursor, 20, controller.signal)
      if (current !== listGeneration || controller.signal.aborted) return
      items.value = cursor ? [...items.value, ...page.items] : page.items; nextCursor.value = page.nextCursor
    } catch (cause) {
      if (current !== listGeneration || controller.signal.aborted) return
      listError.value = cause instanceof Error ? cause.message : '通知加载失败'
      requestId.value = cause instanceof APIError ? cause.requestId : ''
      if (cause instanceof APIError && cause.status === 401) stop()
    } finally { if (current === listGeneration) { listLoading.value = false; listController = undefined } }
  }
  async function markRead(id: string) {
    mutationController?.abort(); const controller = new AbortController(); mutationController = controller
    const current = ++mutationGeneration, userId = activeUserId.value
    try {
      await markNotificationRead(id, controller.signal)
      if (current !== mutationGeneration || controller.signal.aborted || activeUserId.value !== userId) return
      const found = items.value.find((entry) => entry.id === id)
      if (found && !found.readAt) { found.readAt = new Date().toISOString(); unreadCount.value = Math.max(0, unreadCount.value - 1) }
    } finally { if (mutationController === controller) mutationController = undefined }
  }
  async function markAllRead() {
    mutationController?.abort(); const controller = new AbortController(); mutationController = controller
    const current = ++mutationGeneration, userId = activeUserId.value
    try {
      await markAllNotificationsRead(controller.signal)
      if (current !== mutationGeneration || controller.signal.aborted || activeUserId.value !== userId) return
      const now = new Date().toISOString()
      items.value.forEach((entry) => { if (!entry.readAt) entry.readAt = now }); unreadCount.value = 0
    } finally { if (mutationController === controller) mutationController = undefined }
  }
  return { unreadCount, badgeText: computed(() => unreadCount.value > 99 ? '99+' : String(unreadCount.value)), activeUserId, items, nextCursor, listLoading, listError, requestId, start, stop, refresh, list, cancelList, cancelMutations, markRead, markAllRead }
})
