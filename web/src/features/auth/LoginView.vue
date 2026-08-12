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
let challengeGeneration = 0
let challengeController: AbortController | undefined

function clearChallengeImage() {
  if (objectUrl) URL.revokeObjectURL(objectUrl)
  objectUrl = ''
  challengeId.value = ''
  challengeImageUrl.value = ''
}

async function refreshChallenge() {
  const generation = ++challengeGeneration
  challengeController?.abort()
  const controller = new AbortController()
  challengeController = controller
  message.value = ''
  clearChallengeImage()
  try {
    const response = await fetch('/api/v1/auth/challenge', { credentials: 'include', headers: { Accept: 'image/png' }, cache: 'no-store', signal: controller.signal })
    const id = response.headers.get('X-Challenge-ID')
    if (!response.ok || !id) throw new Error('challenge')
    const nextObjectUrl = URL.createObjectURL(await response.blob())
    if (generation !== challengeGeneration) {
      URL.revokeObjectURL(nextObjectUrl)
      return
    }
    challengeId.value = id
    objectUrl = nextObjectUrl
    challengeImageUrl.value = nextObjectUrl
    challengeAnswer.value = ''
  } catch {
    if (generation !== challengeGeneration || controller.signal.aborted) return
    clearChallengeImage()
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

onBeforeUnmount(() => { challengeGeneration += 1; challengeController?.abort(); clearChallengeImage() })
</script>

<template>
  <main class="auth-page">
    <div class="auth-decoration auth-decoration-one" aria-hidden="true"></div><div class="auth-decoration auth-decoration-two" aria-hidden="true"></div><div class="auth-grid" aria-hidden="true"></div>
    <div class="auth-shell">
      <header class="auth-brand" aria-label="HappyLearn">
        <span class="auth-logo" aria-hidden="true">H</span>
        <strong>HappyLearn</strong><small>高中理科学习平台</small>
      </header>
      <section class="auth-card" aria-labelledby="login-title">
        <p class="eyebrow"><span aria-hidden="true"></span> 欢迎回来</p>
        <h1 id="login-title">回到你的学习节奏</h1>
        <p class="intro">数学与物理的每一次练习，都有清晰的下一步。</p>
        <form @submit.prevent="submit" novalidate>
          <label for="login-username">账号</label>
          <div class="input-wrap"><input id="login-username" v-model="username" aria-label="账号" autocomplete="username" inputmode="text" placeholder="请输入账号" :disabled="pending" /></div>
          <label for="login-password">密码</label>
          <div class="input-wrap"><input id="login-password" v-model="password" aria-label="密码" type="password" autocomplete="current-password" placeholder="请输入密码" :disabled="pending" /></div>
          <div v-if="challengeImageUrl" class="challenge" aria-live="polite">
            <label for="challenge-answer">验证码答案</label>
            <div class="challenge-row"><img :src="challengeImageUrl" alt="登录验证码" /><button type="button" class="text-button" aria-label="刷新验证码" :disabled="pending" @click="refreshChallenge">换一张</button></div>
            <input id="challenge-answer" v-model="challengeAnswer" aria-label="验证码答案" autocomplete="off" placeholder="请输入图中答案" :disabled="pending" />
          </div>
          <p v-if="message" class="form-message" role="alert"><span class="alert-icon" aria-hidden="true">!</span><span>{{ message }}</span></p>
          <button class="primary-button" type="submit" :disabled="pending"><span v-if="pending" class="spinner" aria-hidden="true"></span><span>{{ pending ? '正在验证…' : '登录 HappyLearn' }}</span><span v-if="!pending" class="button-arrow" aria-hidden="true">›</span></button>
        </form>
      </section>
      <p class="auth-footnote"><span aria-hidden="true"></span> 安全连接 · 专注学习</p>
    </div>
  </main>
</template>

<style scoped>
.auth-page{position:relative;isolation:isolate;min-height:100vh;display:grid;place-items:center;overflow:hidden;padding:32px 20px;background:linear-gradient(145deg,var(--hl-bg),color-mix(in srgb,var(--hl-primary-soft) 36%,var(--hl-bg)) 52%,var(--hl-bg));color:var(--hl-text)}
.auth-grid{position:absolute;z-index:-2;inset:0;opacity:.3;background-image:linear-gradient(color-mix(in srgb,var(--hl-border) 58%,transparent) 1px,transparent 1px),linear-gradient(90deg,color-mix(in srgb,var(--hl-border) 58%,transparent) 1px,transparent 1px);background-size:42px 42px;mask-image:linear-gradient(to bottom,black,transparent 78%)}
.auth-decoration{position:absolute;z-index:-1;width:360px;height:360px;border-radius:50%;filter:blur(90px);opacity:.32}.auth-decoration-one{top:-150px;right:-90px;background:#2dd4bf}.auth-decoration-two{bottom:-190px;left:-100px;background:#38bdf8;opacity:.18}
.auth-shell{width:min(100%,430px)}.auth-brand{display:grid;justify-items:center;margin-bottom:26px}.auth-logo{display:grid;place-items:center;width:58px;height:58px;margin-bottom:12px;border:1px solid rgba(255,255,255,.3);border-radius:17px;background:linear-gradient(135deg,#0f766e,#14b8a6);color:#fff;box-shadow:0 14px 30px rgba(13,148,136,.25);font-size:1.4rem;font-weight:850;letter-spacing:-.05em}.auth-brand strong{font-size:1.36rem;letter-spacing:-.03em}.auth-brand small{margin-top:4px;color:var(--hl-text-muted);font-size:.76rem;letter-spacing:.08em}
.auth-card{width:100%;padding:34px;border:1px solid color-mix(in srgb,var(--hl-border) 86%,transparent);border-radius:20px;background:var(--hl-surface);box-shadow:var(--hl-shadow);backdrop-filter:blur(18px);-webkit-backdrop-filter:blur(18px)}.eyebrow{display:flex;align-items:center;gap:7px;margin:0;color:var(--hl-primary-strong);font-size:.76rem;font-weight:750;letter-spacing:.1em}.eyebrow span{width:7px;height:7px;border-radius:50%;background:var(--hl-accent);box-shadow:0 0 0 4px var(--hl-primary-soft)}.auth-card h1{margin:11px 0 8px;font-size:clamp(1.65rem,5vw,2.05rem);line-height:1.25;letter-spacing:-.035em}.intro{margin:0 0 27px;color:var(--hl-text-muted);font-size:.9rem;line-height:1.65}label{display:block;margin:17px 0 7px;color:var(--hl-text);font-size:.82rem;font-weight:680}.input-wrap{position:relative}input{box-sizing:border-box;width:100%;height:45px;padding:10px 13px;border:1px solid var(--hl-border-strong);border-radius:10px;outline:0;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit;font-size:.9rem;transition:border-color .16s ease,box-shadow .16s ease}input::placeholder{color:var(--hl-text-soft)}input:focus{border-color:var(--hl-accent);box-shadow:0 0 0 3px var(--hl-primary-soft)}input:disabled{opacity:.65;cursor:wait}
.primary-button{display:flex;align-items:center;justify-content:center;gap:9px;width:100%;height:46px;margin-top:24px;padding:0 15px;border:0;border-radius:10px;background:linear-gradient(135deg,#0f766e,#0d9488);box-shadow:0 9px 22px rgba(13,148,136,.2);color:#fff;font:inherit;font-size:.9rem;font-weight:750;cursor:pointer;transition:transform .16s ease,box-shadow .16s ease}.primary-button:hover:not(:disabled){transform:translateY(-1px);box-shadow:0 12px 27px rgba(13,148,136,.28)}.primary-button:disabled,.text-button:disabled{opacity:.65;cursor:wait}.spinner{width:16px;height:16px;border:2px solid rgba(255,255,255,.36);border-top-color:#fff;border-radius:50%;animation:spin .8s linear infinite}.button-arrow{font-size:1.3rem;line-height:1}
.form-message{display:flex;align-items:flex-start;gap:8px;margin:16px 0 0;padding:10px 11px;border:1px solid color-mix(in srgb,var(--hl-danger) 22%,transparent);border-radius:9px;background:var(--hl-danger-soft);color:var(--hl-danger);font-size:.82rem;line-height:1.5}.alert-icon{display:grid;flex:0 0 17px;place-items:center;width:17px;height:17px;margin-top:1px;border:1px solid currentColor;border-radius:50%;font-size:.68rem;font-weight:800;line-height:1}.challenge{margin-top:8px}.challenge-row{display:flex;align-items:center;gap:12px}.challenge img{height:46px;min-width:130px;border:1px solid var(--hl-border);border-radius:8px;background:#fff}.challenge>input{margin-top:9px}.text-button{padding:7px;border:0;background:none;color:var(--hl-primary-strong);font:inherit;font-size:.82rem;font-weight:650;text-decoration:underline;text-underline-offset:3px;cursor:pointer}.auth-footnote{display:flex;align-items:center;justify-content:center;gap:8px;margin:18px 0 0;color:var(--hl-text-soft);font-size:.72rem}.auth-footnote span{width:6px;height:6px;border-radius:50%;background:#22c55e;box-shadow:0 0 0 3px color-mix(in srgb,#22c55e 16%,transparent)}
@keyframes spin{to{transform:rotate(360deg)}}
@media(max-width:520px){.auth-page{align-items:start;padding:25px 15px}.auth-brand{margin-bottom:20px}.auth-logo{width:52px;height:52px}.auth-card{padding:27px 22px;border-radius:17px}.intro{margin-bottom:23px}}
</style>
