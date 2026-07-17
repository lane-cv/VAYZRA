<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { request, type UserView } from '../../api/client'
import { useSessionStore } from '../../stores/session'

const router = useRouter()
const session = useSessionStore()
const currentPassword = ref('')
const newPassword = ref('')
const confirmation = ref('')
const pending = ref(false)
const message = ref('')
const requestId = ref('')

function clearPasswords() { currentPassword.value = ''; newPassword.value = ''; confirmation.value = '' }
function submit() { void performSubmit() }

async function performSubmit() {
  if (pending.value) return
  requestId.value = ''
  if (!currentPassword.value || !newPassword.value || !confirmation.value) { message.value = '请填写全部密码字段'; return }
  if (newPassword.value !== confirmation.value) { message.value = '两次输入的新密码不一致'; return }
  pending.value = true; message.value = ''
  try {
    await request<UserView>('/auth/change-password', { method: 'POST', json: { currentPassword: currentPassword.value, newPassword: newPassword.value } })
    clearPasswords()
    await session.refresh()
    if (!session.user) { await router.replace('/login'); return }
    await router.replace(session.user.role === 'admin' ? '/admin' : '/student')
  } catch (error) {
    clearPasswords()
    message.value = '密码修改未完成，请检查当前密码后重试'
    if (typeof error === 'object' && error !== null && typeof (error as { requestId?: unknown }).requestId === 'string') requestId.value = (error as { requestId: string }).requestId
  } finally { pending.value = false }
}
</script>

<template>
  <main class="auth-page">
    <section class="auth-card" aria-labelledby="change-title">
      <p class="eyebrow">首次登录保护</p><h1 id="change-title">设置新的登录密码</h1><p class="intro">为了保护你的学习记录，请先完成密码更新。</p>
      <form @submit.prevent="submit" novalidate>
        <label for="current-password">当前密码</label><input id="current-password" v-model="currentPassword" aria-label="当前密码" type="password" autocomplete="current-password" :disabled="pending" />
        <label for="new-password">新密码</label><input id="new-password" v-model="newPassword" aria-label="新密码" type="password" autocomplete="new-password" :disabled="pending" />
        <label for="confirm-password">确认新密码</label><input id="confirm-password" v-model="confirmation" aria-label="确认新密码" type="password" autocomplete="new-password" :disabled="pending" />
        <p v-if="message" class="form-message" role="alert">{{ message }}<span v-if="requestId"> 支持编号：{{ requestId }}</span></p>
        <button class="primary-button" type="submit" :disabled="pending">{{ pending ? '正在更新…' : '保存新密码' }}</button>
      </form>
    </section>
  </main>
</template>

<style scoped>
.auth-page{min-height:100vh;display:grid;place-items:center;padding:24px;background:#f7f9fc;color:#17243d}.auth-card{width:min(100%,420px);padding:clamp(28px,6vw,48px);border:1px solid #dbe4f0;border-radius:20px;background:#fff;box-shadow:0 20px 50px #2e517022}.eyebrow{margin:0;color:#1673b9;font-size:.86rem;font-weight:700;letter-spacing:.06em}.auth-card h1{margin:10px 0 8px;font-size:clamp(1.8rem,5vw,2.4rem)}.intro{margin:0 0 30px;color:#53627a;line-height:1.65}label{display:block;margin:18px 0 7px;font-weight:650}input{box-sizing:border-box;width:100%;padding:12px 13px;border:1px solid #b8c7db;border-radius:9px;font:inherit}.primary-button{width:100%;margin-top:24px;padding:13px;border:0;border-radius:9px;background:#166cbb;color:#fff;font:inherit;font-weight:700;cursor:pointer}.primary-button:disabled{opacity:.65;cursor:wait}.form-message{margin:16px 0 0;color:#b42318;font-size:.93rem}
</style>