<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { request, type UserView } from '../../api/client'
import { useSessionStore } from '../../stores/session'

const router = useRouter()
const session = useSessionStore()
const username = ref('')
const password = ref('')
const challengeAnswer = ref('')
const challengeId = ref('')
const challengeImageUrl = ref('')
const pending = ref(false)
const message = ref('')
let objectUrl = ''

function clearChallengeImage() {
  if (objectUrl) URL.revokeObjectURL(objectUrl)
  objectUrl = ''
  challengeImageUrl.value = ''
}

async function refreshChallenge() {
  message.value = ''
  clearChallengeImage()
  try {
    const response = await fetch('/api/v1/auth/challenge', { credentials: 'include', headers: { Accept: 'image/png' }, cache: 'no-store' })
    const id = response.headers.get('X-Challenge-ID')
    if (!response.ok || !id) throw new Error('challenge')
    challengeId.value = id
    objectUrl = URL.createObjectURL(await response.blob())
    challengeImageUrl.value = objectUrl
    challengeAnswer.value = ''
  } catch {
    message.value = '验证码暂时无法加载，请稍后重试'
  }
}

function submit() { void performSubmit() }

async function performSubmit() {
  if (pending.value) return
  if (!username.value.trim() || !password.value) { message.value = '请填写账号和密码'; return }
  if (challengeImageUrl.value && !challengeAnswer.value.trim()) { message.value = '请填写验证码'; return }
  pending.value = true
  message.value = ''
  try {
    const user = await request<UserView>('/auth/login', { method: 'POST', json: { username: username.value.trim(), password: password.value, ...(challengeId.value ? { challengeId: challengeId.value, challengeAnswer: challengeAnswer.value.trim() } : {}) } })
    password.value = ''
    challengeAnswer.value = ''
    session.setUser(user)
    await router.replace(user.mustChangePassword ? '/change-password' : user.role === 'admin' ? '/admin' : '/student')
  } catch (error) {
    password.value = ''
    challengeAnswer.value = ''
    if (hasCode(error, 'login_challenge_required')) {
      message.value = '为保护账号安全，请完成验证码后继续'
      await refreshChallenge()
    } else {
      message.value = '账号或密码不正确，请重试'
    }
  } finally { pending.value = false }
}

function hasCode(error: unknown, code: string): boolean { return typeof error === 'object' && error !== null && (error as { code?: unknown }).code === code }

onBeforeUnmount(clearChallengeImage)
</script>

<template>
  <main class="auth-page">
    <section class="auth-card" aria-labelledby="login-title">
      <p class="eyebrow">HappyLearn 高中理科辅导</p>
      <h1 id="login-title">回到你的学习节奏</h1>
      <p class="intro">数学与物理的每一次练习，都有清晰的下一步。</p>
      <form @submit.prevent="submit" novalidate>
        <label for="login-username">账号</label>
        <input id="login-username" v-model="username" aria-label="账号" autocomplete="username" inputmode="text" :disabled="pending" />
        <label for="login-password">密码</label>
        <input id="login-password" v-model="password" aria-label="密码" type="password" autocomplete="current-password" :disabled="pending" />
        <div v-if="challengeImageUrl" class="challenge" aria-live="polite">
          <label for="challenge-answer">验证码答案</label>
          <div class="challenge-row"><img :src="challengeImageUrl" alt="登录验证码" /><button type="button" class="text-button" aria-label="刷新验证码" :disabled="pending" @click="refreshChallenge">换一张</button></div>
          <input id="challenge-answer" v-model="challengeAnswer" aria-label="验证码答案" autocomplete="off" :disabled="pending" />
        </div>
        <p v-if="message" class="form-message" role="alert">{{ message }}</p>
        <button class="primary-button" type="submit" :disabled="pending">{{ pending ? '正在验证…' : '登录 HappyLearn' }}</button>
      </form>
    </section>
  </main>
</template>

<style scoped>
.auth-page{min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(circle at top right,#cce7ff 0,transparent 35%),#f7f9fc;color:#17243d}.auth-card{width:min(100%,420px);padding:clamp(28px,6vw,48px);border:1px solid #dbe4f0;border-radius:20px;background:#fff;box-shadow:0 20px 50px #2e517022}.eyebrow{margin:0;color:#1673b9;font-size:.86rem;font-weight:700;letter-spacing:.06em}.auth-card h1{margin:10px 0 8px;font-size:clamp(1.8rem,5vw,2.4rem)}.intro{margin:0 0 30px;color:#53627a;line-height:1.65}label{display:block;margin:18px 0 7px;font-weight:650}input{box-sizing:border-box;width:100%;padding:12px 13px;border:1px solid #b8c7db;border-radius:9px;font:inherit}.primary-button{width:100%;margin-top:24px;padding:13px;border:0;border-radius:9px;background:#166cbb;color:#fff;font:inherit;font-weight:700;cursor:pointer}.primary-button:disabled,.text-button:disabled{opacity:.65;cursor:wait}.form-message{margin:16px 0 0;color:#b42318;font-size:.93rem}.challenge{margin-top:8px}.challenge-row{display:flex;align-items:center;gap:12px}.challenge img{height:44px;min-width:130px;border:1px solid #d5deeb;border-radius:6px;background:#fff}.text-button{padding:6px;border:0;background:none;color:#166cbb;font:inherit;text-decoration:underline;cursor:pointer}
</style>