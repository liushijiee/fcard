<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LockKeyhole } from '@lucide/vue'
import { userApi } from '../lib/api'
import { setUserSession } from '../lib/user-session'
import ErrorBanner from '../components/ErrorBanner.vue'

const router = useRouter()
const route = useRoute()
const email = ref('')
const password = ref('')
const loading = ref(false)
const error = ref<unknown>(null)
let active = true
onBeforeUnmount(() => { active = false; password.value = '' })
async function login(): Promise<void> {
  if (loading.value) return
  loading.value = true
  error.value = null
  try {
    const result = await userApi.login(email.value.trim(), password.value)
    if (!active) return
    setUserSession(result)
    await router.replace(result.user.must_change_password ? '/portal/security' : '/portal')
  } catch (failure) { if (active) error.value = failure }
  finally { password.value = ''; loading.value = false }
}
</script>

<template>
  <div class="portal-login"><section class="panel portal-login-card"><RouterLink class="brand" to="/portal"><span class="brand-mark">f<span></span></span><span>fcard.</span></RouterLink><p class="eyebrow">PERSONAL WORKSPACE · SANDBOX</p><span class="login-lock"><LockKeyhole :size="25" /></span><h1>登录用户空间</h1><p class="muted">账号由管理员开通，暂不开放自助注册。</p><div v-if="route.query.changed === '1'" class="success-banner" role="status">密码已修改，请使用新密码重新登录。</div><ErrorBanner :error="error" /><form @submit.prevent="login"><label class="form-field">用户邮箱<input v-model="email" type="email" autocomplete="username" required maxlength="254" :disabled="loading" /></label><label class="form-field">密码<input v-model="password" type="password" autocomplete="current-password" required :disabled="loading" /></label><button class="button primary" type="submit" :disabled="loading"><span v-if="loading" class="spinner"></span>{{ loading ? '登录中…' : '登录用户空间' }}</button></form><p class="muted">首次登录须修改管理员交付的临时密码。</p><RouterLink to="/login">前往管理员登录</RouterLink></section></div>
</template>
