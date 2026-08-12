<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { storeToRefs } from 'pinia'
import { useApplicationUpdateStore } from '../../stores/applicationUpdates'
import type { UpdatePhase, UpdateState } from './types'

const root = ref<HTMLElement>()
const trigger = ref<HTMLButtonElement>()
const panelOpen = ref(false)
const panelId = `application-update-${useId()}`
const updates = useApplicationUpdateStore()
const {
  status,
  checking,
  operation,
  error,
  isTransient,
  busy,
  retryBusy,
  currentVersion,
  hasUpdate,
  progress,
  displayMessage,
  retryLabel,
} = storeToRefs(updates)

const stateLabels: Record<UpdateState, string> = {
  disabled: '未启用',
  unknown: '待检查',
  checking: '检查中',
  current: '已是最新',
  available: '有新版本',
  updating: '更新中',
  success: '操作完成',
  failed: '操作失败',
  blocked: '已阻止',
}

const phaseLabels: Record<UpdatePhase, string> = {
  idle: '等待开始',
  checking: '检查版本',
  fetching: '拉取元数据',
  preparing: '准备更新',
  building: '构建服务',
  switching: '切换服务',
  verifying: '健康验证',
  merging: '合并发布',
  recovering: '恢复上一版本',
  complete: '已完成',
  failed: '已失败',
}

function formatDate(value: string | null): string {
  return value ? new Date(value).toLocaleString('zh-CN') : ''
}

function togglePanel() {
  panelOpen.value = !panelOpen.value
}

function closePanel(restoreFocus = false) {
  if (!panelOpen.value) return
  panelOpen.value = false
  if (restoreFocus) void nextTick(() => trigger.value?.focus())
}

function handleDocumentClick(event: MouseEvent) {
  const target = event.target
  if (panelOpen.value && target instanceof Node && !root.value?.contains(target)) closePanel()
}

function handleDocumentKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && panelOpen.value) closePanel(true)
}

onMounted(() => {
  updates.connect()
  document.addEventListener('click', handleDocumentClick)
  document.addEventListener('keydown', handleDocumentKeydown)
})

onBeforeUnmount(() => {
  updates.disconnect()
  document.removeEventListener('click', handleDocumentClick)
  document.removeEventListener('keydown', handleDocumentKeydown)
})
</script>

<template>
  <div ref="root" class="application-update-badge">
    <button
      ref="trigger"
      data-testid="application-update-trigger"
      class="version-trigger"
      :class="{ available: hasUpdate, busy }"
      type="button"
      aria-haspopup="dialog"
      :aria-controls="panelId"
      :aria-expanded="panelOpen"
      :title="hasUpdate ? '有新版本可用' : '查看应用版本'"
      @click="togglePanel"
    >
      <span class="version-mark" aria-hidden="true">↟</span>
      <span class="version-number">v{{ currentVersion }}</span>
      <span v-if="hasUpdate" data-testid="update-available-indicator" class="update-dot" aria-label="有新版本"></span>
      <span v-else-if="busy" class="busy-dot" aria-label="更新状态处理中"></span>
    </button>

    <Transition name="update-panel">
      <section
        v-if="panelOpen"
        :id="panelId"
        class="version-panel version-panel-below"
        role="dialog"
        data-placement="bottom-start"
        aria-label="应用版本与更新"
      >
        <header class="panel-heading">
          <div>
            <p class="panel-eyebrow">APPLICATION OTA</p>
            <h2>版本更新</h2>
          </div>
          <button
            data-testid="refresh-update"
            class="icon-button"
            type="button"
            :disabled="busy"
            aria-label="立即检查更新"
            @click="updates.refresh(true)"
          >
            <span :class="{ spinning: checking }" aria-hidden="true">↻</span>
          </button>
        </header>

        <div class="version-overview">
          <div>
            <span>当前版本</span>
            <strong>v{{ currentVersion }}</strong>
          </div>
          <div>
            <span>最新版本</span>
            <strong>{{ status?.latestVersion ? `v${status.latestVersion}` : '—' }}</strong>
          </div>
        </div>

        <div v-if="status" class="state-line">
          <span class="state-pill" :class="`state-${status.state}`">{{ stateLabels[status.state] }}</span>
          <span>{{ status.channel }} · {{ status.ref || '默认分支' }}</span>
        </div>

        <p class="status-message" :role="error ? undefined : 'status'" aria-live="polite">{{ displayMessage }}</p>
        <p v-if="error" class="error-message" role="alert">{{ error }}</p>
        <p v-if="status?.dirty" class="warning-message" role="alert">部署目录存在未提交修改，在线更新已阻止。</p>

        <div v-if="status && isTransient" class="progress-block">
          <div class="progress-copy">
            <span>{{ phaseLabels[status.phase] }}</span>
            <span>{{ progress }}%</span>
          </div>
          <div
            class="progress-track"
            role="progressbar"
            aria-label="更新进度"
            aria-valuemin="0"
            aria-valuemax="100"
            :aria-valuenow="progress"
          >
            <span :style="{ width: `${progress}%` }"></span>
          </div>
        </div>

        <section v-if="status?.releaseName || status?.releaseNotes" class="release-card" aria-label="版本说明">
          <div class="release-heading">
            <strong>{{ status.releaseName || `v${status.latestVersion}` }}</strong>
            <time v-if="status.publishedAt" :datetime="status.publishedAt">{{ formatDate(status.publishedAt) }}</time>
          </div>
          <p v-if="status.releaseNotes" class="release-notes">{{ status.releaseNotes }}</p>
          <a
            v-if="status.releaseURL"
            :href="status.releaseURL"
            target="_blank"
            rel="noopener noreferrer"
          >查看完整发布说明 ↗</a>
        </section>

        <div class="update-actions">
          <button
            v-if="error"
            data-testid="retry-update"
            class="primary-action"
            type="button"
            :disabled="retryBusy"
            @click="updates.retry"
          >{{ retryLabel }}</button>
          <template v-else>
            <button
              v-if="status?.state === 'success'"
              data-testid="reload-application"
              class="primary-action"
              type="button"
              @click="updates.reloadPage"
            >重新加载页面</button>
            <button
              v-if="status?.updateAvailable"
              data-testid="apply-update"
              class="primary-action"
              type="button"
              :disabled="status.dirty || busy"
              @click="updates.runAction('update')"
            >{{ operation === 'update' || isTransient ? '更新中…' : `更新到 v${status.latestVersion || '最新版本'}` }}</button>
            <button
              v-if="status?.canRollback && status.previousVersion"
              data-testid="rollback-update"
              class="secondary-action"
              type="button"
              :disabled="busy"
              @click="updates.runAction('rollback')"
            >回滚到 v{{ status.previousVersion }}</button>
          </template>
        </div>
      </section>
    </Transition>
  </div>
</template>

<style scoped>
.application-update-badge{position:relative;width:100%;min-width:0;color:var(--hl-text,#dce8ee)}.version-trigger{display:flex;width:100%;max-width:100%;align-items:center;gap:7px;border:1px solid var(--hl-border-strong,#355364);border-radius:9px;background:var(--hl-bg-deep,#203b4b);color:var(--hl-text,#dce8ee);padding:7px 10px;font:inherit;font-size:.78rem;line-height:1;cursor:pointer;transition:border-color .18s ease,background .18s ease,color .18s ease}.version-trigger:hover,.version-trigger:focus-visible{border-color:var(--hl-accent,#4cb5a7);background:var(--hl-primary-strong,#254958);outline:none;color:var(--hl-surface-solid,#fff)}.version-trigger.available{border-color:var(--hl-accent,#45a999);background:var(--hl-primary-soft,#183f43);color:var(--hl-text,#c9fff6)}.version-trigger.busy{border-color:var(--hl-border-strong,#527080)}.version-mark{display:grid;place-items:center;width:18px;height:18px;border-radius:6px;background:var(--hl-primary-soft,#2b5a60);color:var(--hl-accent,#77d7c9);font-weight:900}.version-number{min-width:0;overflow:hidden;text-overflow:ellipsis;font-weight:800;white-space:nowrap}.update-dot,.busy-dot{width:7px;height:7px;flex:0 0 auto;border-radius:50%}.update-dot{margin-left:auto;background:var(--hl-accent,#52d6be);box-shadow:0 0 0 4px color-mix(in srgb,var(--hl-accent,#52d6be) 15%,transparent)}.busy-dot{margin-left:auto;background:var(--hl-text-soft,#94a9b5);animation:pulse 1.2s ease-in-out infinite}.version-panel{position:absolute;z-index:30;top:calc(100% + 9px);left:0;box-sizing:border-box;width:330px;max-width:calc(100vw - 32px);max-height:min(640px,calc(100vh - 96px));overflow-x:hidden;overflow-y:auto;border:1px solid var(--hl-border,#cbd9de);border-radius:15px;background:var(--hl-surface-solid,#f8fbfb);color:var(--hl-text,#20323b);box-shadow:var(--hl-shadow,0 18px 46px #0d202b38)}.panel-heading{display:flex;align-items:center;justify-content:space-between;padding:16px 17px 13px;border-bottom:1px solid var(--hl-border,#dce6e8);background:var(--hl-surface-muted,#eef6f5)}.panel-heading h2{margin:2px 0 0;color:var(--hl-text,#1d3a42);font-size:1.02rem}.panel-eyebrow{margin:0;color:var(--hl-primary-strong,#238b7d);font-size:.64rem;font-weight:900;letter-spacing:.13em}.icon-button{display:grid;place-items:center;width:32px;height:32px;border:1px solid var(--hl-border-strong,#c4d6d7);border-radius:9px;background:var(--hl-surface,#fff);color:var(--hl-text-muted,#3b6970);font:inherit;font-size:1.15rem;cursor:pointer}.icon-button:disabled{opacity:.5;cursor:not-allowed}.version-overview{display:grid;grid-template-columns:1fr 1fr;gap:9px;padding:14px 16px 10px}.version-overview div{display:grid;gap:4px;padding:10px;border:1px solid var(--hl-border,#dce6e8);border-radius:10px;background:var(--hl-surface,#fff)}.version-overview span{color:var(--hl-text-soft,#687d85);font-size:.7rem}.version-overview strong{color:var(--hl-text,#1f4d51);font-size:.98rem}.state-line{display:flex;align-items:center;gap:8px;padding:0 16px;color:var(--hl-text-soft,#687d85);font-size:.72rem}.state-pill{display:inline-flex;border-radius:999px;background:var(--hl-surface-muted,#e5ecee);color:var(--hl-text-muted,#51666d);padding:4px 8px;font-weight:800}.state-current,.state-success{background:var(--hl-success-soft,#dff4eb);color:var(--hl-success,#20735f)}.state-available{background:var(--hl-warning-soft,#fff2d9);color:var(--hl-warning,#8a5600)}.state-checking,.state-updating{background:var(--hl-primary-soft,#e0edf1);color:var(--hl-primary-strong,#315f70)}.state-failed,.state-blocked{background:var(--hl-danger-soft,#fae7e4);color:var(--hl-danger,#a14439)}.state-disabled,.state-unknown{background:var(--hl-surface-muted,#e9eef0);color:var(--hl-text-muted,#63747b)}.status-message,.error-message,.warning-message{margin:10px 16px 0;color:var(--hl-text-muted,#52686f);font-size:.78rem;line-height:1.45}.error-message,.warning-message{padding:9px 10px;border:1px solid var(--hl-danger,#edc5bf);border-radius:9px;background:var(--hl-danger-soft,#fff1ef);color:var(--hl-danger,#963b32)}.warning-message{border-color:var(--hl-warning,#e9d8a9);background:var(--hl-warning-soft,#fff9e8);color:var(--hl-warning,#786029)}.progress-block{display:grid;gap:6px;margin:12px 16px 0}.progress-copy{display:flex;justify-content:space-between;color:var(--hl-text-muted,#47666e);font-size:.72rem;font-weight:750}.progress-track{height:7px;overflow:hidden;border-radius:99px;background:var(--hl-surface-muted,#dbe7e8)}.progress-track span{display:block;height:100%;border-radius:inherit;background:linear-gradient(90deg,var(--hl-primary,#2b8e82),var(--hl-accent,#58c7b7));transition:width .25s ease}.release-card{margin:13px 16px 0;padding:11px;border:1px solid var(--hl-border,#d8e5e6);border-radius:10px;background:var(--hl-surface,#fff)}.release-heading{display:grid;gap:3px}.release-heading strong{color:var(--hl-text,#254850);font-size:.82rem}.release-heading time{color:var(--hl-text-soft,#7a8d93);font-size:.68rem}.release-notes{max-height:92px;overflow:auto;margin:8px 0;color:var(--hl-text-muted,#536970);font-size:.74rem;line-height:1.5;white-space:pre-wrap}.release-card a{color:var(--hl-primary-strong,#1f8175);font-size:.72rem;font-weight:800;text-decoration:none}.release-card a:hover{text-decoration:underline}.update-actions{display:grid;gap:8px;padding:14px 16px 16px}.primary-action,.secondary-action{border:0;border-radius:9px;padding:9px 12px;font:inherit;font-size:.78rem;font-weight:850;cursor:pointer}.primary-action{background:var(--hl-primary,#207e73);color:var(--hl-on-primary,#fff)}.primary-action:hover{background:var(--hl-primary-strong,#176a61);color:var(--hl-surface-solid,#0f172a)}.secondary-action{border:1px solid var(--hl-border-strong,#c5d3d6);background:var(--hl-surface-muted,#edf2f3);color:var(--hl-text-muted,#4b6067)}.primary-action:disabled,.secondary-action:disabled{opacity:.52;cursor:not-allowed}.update-panel-enter-active,.update-panel-leave-active{transition:opacity .16s ease,transform .16s ease}.update-panel-enter-from,.update-panel-leave-to{opacity:0;transform:translateY(-5px) scale(.98)}.spinning{display:inline-block;animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@keyframes pulse{50%{opacity:.35}}@media(max-width:380px){.version-panel{width:calc(100vw - 32px)}.version-overview{grid-template-columns:1fr}}@media(prefers-reduced-motion:reduce){.version-trigger,.progress-track span,.update-panel-enter-active,.update-panel-leave-active{transition:none}.busy-dot,.spinning{animation:none}}
</style>
