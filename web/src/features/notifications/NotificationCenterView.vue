<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { APIError } from '../../api/client'
import { useNotificationStore } from '../../stores/notifications'
import { useSessionStore } from '../../stores/session'
import { safeNotificationTarget, type NotificationItem } from './types'
const notifications = useNotificationStore(), session = useSessionStore(), router = useRouter()
const actionPending = ref(false), actionError = ref(''), actionRequestId = ref('')
async function open(item: NotificationItem) { const target = safeNotificationTarget(item.targetPath, session.user?.role); if (!target) return; try { await notifications.markRead(item.id) } catch { /* An idempotent read failure must not trap navigation. */ } await router.push(target) }
async function markOne(id: string) { actionPending.value = true; actionError.value = ''; actionRequestId.value = ''; try { await notifications.markRead(id) } catch (cause) { actionError.value = cause instanceof Error ? cause.message : '操作失败'; actionRequestId.value = cause instanceof APIError ? cause.requestId : '' } finally { actionPending.value = false } }
async function markAll() { actionPending.value = true; actionError.value = ''; actionRequestId.value = ''; try { await notifications.markAllRead() } catch (cause) { actionError.value = cause instanceof Error ? cause.message : '操作失败'; actionRequestId.value = cause instanceof APIError ? cause.requestId : '' } finally { actionPending.value = false } }
onMounted(() => void notifications.list())
onBeforeUnmount(() => { notifications.cancelList(); notifications.cancelMutations() })
</script>
<template>
  <section class="notifications" aria-labelledby="notification-title">
    <header><div><p class="eyebrow">消息</p><h1 id="notification-title">通知中心</h1></div><button type="button" :disabled="notifications.listLoading || actionPending || notifications.unreadCount === 0" @click="markAll">全部标为已读</button></header>
    <p v-if="actionError" role="alert">{{ actionError }}<span v-if="actionRequestId">（支持编号：{{ actionRequestId }}）</span></p>
    <p v-if="notifications.listLoading && !notifications.items.length" role="status">正在加载通知…</p>
    <div v-else-if="notifications.listError" role="alert"><p>{{ notifications.listError }}<span v-if="notifications.requestId">（支持编号：{{ notifications.requestId }}）</span></p><button type="button" aria-label="重试加载通知" @click="notifications.list()">重试</button></div>
    <p v-else-if="!notifications.items.length" class="empty">暂时没有通知。</p>
    <ul v-else>
      <li v-for="item in notifications.items" :key="item.id" :class="{ unread: !item.readAt }">
        <a v-if="safeNotificationTarget(item.targetPath, session.user?.role)" :href="safeNotificationTarget(item.targetPath, session.user?.role)" @click.prevent="open(item)"><strong>{{ item.title }}</strong><span>{{ item.summary }}</span><time :datetime="item.createdAt">{{ new Date(item.createdAt).toLocaleString('zh-CN') }}</time></a>
        <div v-else><strong>{{ item.title }}</strong><span>{{ item.summary }}</span><time :datetime="item.createdAt">{{ new Date(item.createdAt).toLocaleString('zh-CN') }}</time></div>
        <button v-if="!item.readAt" type="button" :disabled="actionPending" @click="markOne(item.id)">标为已读</button>
      </li>
    </ul>
    <button v-if="!notifications.listLoading && notifications.nextCursor" type="button" aria-label="加载更多通知" @click="notifications.list(notifications.nextCursor)">加载更多</button>
    <p v-if="notifications.listLoading && notifications.items.length" role="status">正在加载更多通知…</p>
  </section>
</template>
<style scoped>.notifications{max-width:820px}.notifications>header{display:flex;align-items:center;justify-content:space-between;gap:20px}.eyebrow{color:var(--hl-primary-strong);font-weight:700}.notifications h1{margin:.35rem 0 1rem}.notifications button{padding:9px 12px;border:1px solid var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text)}.notifications ul{display:grid;gap:10px;padding:0;list-style:none}.notifications li{position:relative;display:flex;align-items:center;gap:10px;border:1px solid var(--hl-border);border-radius:11px;background:var(--hl-surface-solid)}.notifications li.unread{border-left:4px solid var(--hl-primary)}.notifications a,.notifications li>div{display:grid;flex:1;gap:6px;padding:16px;color:var(--hl-text);text-decoration:none}.notifications span,.notifications time{color:var(--hl-text-muted)}.notifications time{font-size:.82rem}.notifications li>button{margin-right:14px}.empty{padding:28px;border:1px dashed var(--hl-border-strong);border-radius:10px;text-align:center;color:var(--hl-text-muted)}[role=alert]{color:var(--hl-danger)}@media(max-width:560px){.notifications>header{align-items:flex-start;flex-direction:column}.notifications li{align-items:stretch;flex-direction:column}.notifications li>button{margin:0 14px 14px}}</style>
