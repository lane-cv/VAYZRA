<script setup lang="ts">
import { computed, ref } from 'vue'
import { useSessionStore } from '../../stores/session'
import AILimitsEditor from './AILimitsEditor.vue'
import ModelRoutingEditor from './ModelRoutingEditor.vue'
import PromptEditor from './PromptEditor.vue'
import ProviderEditor from './ProviderEditor.vue'

type Tab = 'providers' | 'models' | 'prompts' | 'limits'
const tabs: Array<{ id: Tab; label: string; panel: string }> = [
  { id: 'providers', label: '供应商配置', panel: 'provider-panel' },
  { id: 'models', label: '模型路由', panel: 'model-routing-panel' },
  { id: 'prompts', label: '提示词', panel: 'prompt-panel' },
  { id: 'limits', label: '额度策略', panel: 'limits-panel' },
]
const session = useSessionStore()
const isAdmin = computed(() => session.user?.role === 'admin')
const active = ref<Tab>('providers')

function select(tab: Tab) {
  active.value = tab
}

function keyboardSelect(event: KeyboardEvent, index: number) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  let next = index
  if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length
  if (event.key === 'ArrowRight') next = (index + 1) % tabs.length
  if (event.key === 'Home') next = 0
  if (event.key === 'End') next = tabs.length - 1
  active.value = tabs[next].id
  requestAnimationFrame(() => document.getElementById(`ai-tab-${tabs[next].id}`)?.focus())
}
</script>

<template>
  <section v-if="!isAdmin" class="denied" aria-labelledby="admin-ai-title">
    <h1 id="admin-ai-title">无权访问 AI 管理</h1>
    <p>此功能仅对教师开放。</p>
  </section>
  <section v-else class="page" aria-labelledby="admin-ai-title">
    <header class="page-heading">
      <p class="eyebrow">教师工作台</p>
      <h1 id="admin-ai-title">AI 管理</h1>
      <p>集中管理供应商、模型路由、学科提示词与学生额度。</p>
    </header>
    <div class="tabs" role="tablist" aria-label="AI 管理栏目">
      <button
        v-for="(tab, index) in tabs"
        :id="`ai-tab-${tab.id}`"
        :key="tab.id"
        role="tab"
        type="button"
        :aria-selected="active === tab.id"
        :aria-controls="tab.panel"
        :tabindex="active === tab.id ? 0 : -1"
        @click="select(tab.id)"
        @keydown="keyboardSelect($event, index)"
      >{{ tab.label }}</button>
    </div>
    <div v-if="active === 'providers'" id="provider-panel" role="tabpanel" aria-labelledby="ai-tab-providers"><ProviderEditor /></div>
    <div v-else-if="active === 'models'" id="model-routing-panel" role="tabpanel" aria-labelledby="ai-tab-models"><ModelRoutingEditor /></div>
    <div v-else-if="active === 'prompts'" id="prompt-panel" role="tabpanel" aria-labelledby="ai-tab-prompts"><PromptEditor /></div>
    <div v-else id="limits-panel" role="tabpanel" aria-labelledby="ai-tab-limits"><AILimitsEditor /></div>
  </section>
</template>

<style scoped>
.page{max-width:1180px}.page-heading{margin-bottom:24px}.page-heading h1{margin:.35rem 0;font-size:clamp(1.75rem,4vw,2.55rem)}.page-heading p:not(.eyebrow){margin:0;color:var(--hl-text-muted)}.eyebrow{margin:0;color:var(--hl-primary-strong);font-size:.84rem;font-weight:700;letter-spacing:.06em}.tabs{display:flex;gap:6px;overflow-x:auto;margin-bottom:22px;padding:5px;border:1px solid var(--hl-border);border-radius:11px;background:var(--hl-surface-solid)}.tabs button{flex:0 0 auto;border:0;border-radius:8px;background:transparent;color:var(--hl-text-muted);padding:10px 14px;font:inherit;font-weight:700;cursor:pointer}.tabs button[aria-selected=true]{background:var(--hl-primary);color:#fff}.denied{max-width:650px;padding:32px;border:1px solid #efc1be;border-radius:13px;background:var(--hl-surface-solid)}
</style>
