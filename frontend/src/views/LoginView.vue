<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowRight, LockKeyhole, ShieldCheck, Eye, EyeOff, FlaskConical } from '@lucide/vue'
import { api } from '../lib/api'
import { setSession } from '../lib/session'
import ErrorBanner from '../components/ErrorBanner.vue'

const router = useRouter()
const email = ref('')
const password = ref('')
const visible = ref(false)
const loading = ref(false)
const error = ref<unknown>(null)

async function login(): Promise<void> {
  if (loading.value) return
  loading.value = true
  error.value = null
  try {
    const result = await api.login(email.value.trim(), password.value)
    setSession(result)
    password.value = ''
    await router.replace('/')
  } catch (failure) { error.value = failure; password.value = '' }
  finally { loading.value = false }
}
</script>

<template>
  <div class="login-page">
    <section class="login-story">
      <div class="brand light"><span class="brand-mark">f<span></span></span><span>fcard.</span></div>
      <div class="login-story-body"><span class="login-kicker">YOUR PLATFORM, UNDER CONTROL.</span><h1>让每一次操作，<br />都有迹可循。</h1><p>从用户管理到平台运维，<br />在一个清晰、有序的工作空间中完成。</p><div class="story-graphic" aria-hidden="true"><div class="graphic-ring"></div><div class="graphic-card"><div class="graphic-card-top"><span>fcard.</span><ShieldCheck :size="26" /></div><div class="graphic-lines"><span></span><span></span></div><div class="graphic-card-bottom"><span>ADMIN WORKSPACE</span><span>01 / M1</span></div></div><div class="graphic-caption"><span class="dot"></span> 一个工作空间 · 完整操作记录</div></div></div>
      <span class="login-story-footer">Built for clarity. Designed for control.</span>
    </section>
    <section class="login-form-side">
      <RouterLink to="/portal/login">前往用户登录</RouterLink>
      <span class="environment-badge"><span></span> SANDBOX ENVIRONMENT</span>
      <div class="login-form-wrap"><span class="login-lock"><LockKeyhole :size="25" /></span><p class="eyebrow">WELCOME BACK</p><h2>登录管理控制台</h2><p class="muted">使用已初始化的管理员账号继续。</p><ErrorBanner :error="error" />
        <form @submit.prevent="login">
          <label class="form-field">管理员邮箱<input v-model="email" type="email" name="email" autocomplete="username" placeholder="admin@example.com" required maxlength="254" :disabled="loading" /></label>
          <label class="form-field">密码<span class="password-field"><input v-model="password" :type="visible ? 'text' : 'password'" name="password" autocomplete="current-password" placeholder="请输入管理员密码" required :disabled="loading" /><button type="button" class="icon-button" :aria-label="visible ? '隐藏密码' : '显示密码'" @click="visible = !visible"><EyeOff v-if="visible" :size="18" /><Eye v-else :size="18" /></button></span></label>
          <button class="button primary login-submit" type="submit" :disabled="loading"><span v-if="loading" class="spinner"></span>{{ loading ? '正在登录…' : '进入控制台' }}<ArrowRight v-if="!loading" :size="18" /></button>
        </form>
        <div class="login-hint"><ShieldCheck :size="17" /><p>当前不开放注册。管理员账号需通过后端<br /><code>cmd/admin</code> 初始化，没有默认账号密码。</p></div>
      </div>
      <p class="login-form-footer"><FlaskConical :size="15" /> 此环境仅供测试，不处理真实资金。</p>
    </section>
  </div>
</template>
