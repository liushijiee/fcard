<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { UserRound, ShieldCheck, LogOut, ChevronRight, House, CreditCard, ArrowDownToLine, Globe, ChevronDown } from '@lucide/vue'
import { userApi } from '../lib/api'
import { clearUserSession, userSession, userToken, updateUser } from '../lib/user-session'
import ErrorBanner from './ErrorBanner.vue'

const router = useRouter()
const route = useRoute()
const verifying = ref(true)
const error = ref<unknown>(null)
const loggingOut = ref(false)
let controller: AbortController | null = null
const timer = window.setInterval(() => userToken(), 15000)
onUnmounted(() => { window.clearInterval(timer); controller?.abort() })
watch(userSession, (value, previous) => {
  if (!value) { controller?.abort(); void router.replace('/portal/login') }
  else if (value.access_token !== previous?.access_token) void verify()
  else if (value.user.must_change_password && route.name !== 'portal-security') void router.replace('/portal/security')
}, { flush: 'sync' })

async function verify(): Promise<void> {
  controller?.abort()
  const current = new AbortController()
  controller = current
  const expected = userToken()
  if (!expected) { await router.replace('/portal/login'); return }
  verifying.value = true
  error.value = null
  try {
    const profile = await userApi.me(current.signal)
    if (current.signal.aborted || userToken() !== expected) return
    updateUser(profile, expected)
    if (profile.must_change_password) await router.replace('/portal/security')
  } catch (failure) { if (!current.signal.aborted && userToken() === expected) error.value = failure }
  finally { if (!current.signal.aborted && userToken() === expected) verifying.value = false }
}
onMounted(verify)

async function logout(): Promise<void> {
  if (loggingOut.value) return
  const expected = userToken()
  loggingOut.value = true
  try {
    await userApi.logout()
    if (userToken() === expected) clearUserSession()
  } catch (failure) { if (userToken() === expected) error.value = failure }
  finally { loggingOut.value = false }
}
</script>

<template>
  <div class="portal-shell" :class="{ 'portal-redesign': ['portal-home', 'portal-cards'].includes(String(route.name)) }">
    <header class="portal-header">
      <RouterLink class="brand" to="/portal"><span class="brand-mark">f<span></span></span><span>fcard<span class="brand-period">.</span></span></RouterLink>
      <div class="portal-header-account"><span class="environment-badge"><span></span> SANDBOX</span><RouterLink class="portal-profile" to="/portal/security"><span class="avatar"><UserRound :size="17" /></span><span>{{ userSession?.user.email }}</span><ChevronDown :size="15" /></RouterLink></div>
    </header>
    <div class="portal-body">
      <aside class="portal-sidebar">
        <nav class="portal-nav" aria-label="用户导航">
          <RouterLink to="/portal" aria-label="个人账号"><House :size="21" />首页</RouterLink>
          <RouterLink to="/portal/cards"><CreditCard :size="21" />我的卡片</RouterLink>
          <RouterLink to="/portal/deposits"><ArrowDownToLine :size="21" />Nile 测试收款</RouterLink>
          <RouterLink to="/portal/security"><ShieldCheck :size="21" />账号安全</RouterLink>
        </nav>
        <div class="portal-sidebar-bottom"><RouterLink to="/">管理控制台 <ChevronRight :size="15" /></RouterLink><button :disabled="loggingOut" @click="logout"><LogOut :size="18" />退出用户登录</button><span><Globe :size="18" />CN · 简体中文</span><small>© {{ new Date().getFullYear() }} Fcard</small></div>
      </aside>
      <main class="page-content">
        <div v-if="!['portal-home', 'portal-cards'].includes(String(route.name))" class="page-heading"><div><p class="eyebrow">FCARD PERSONAL ACCOUNT</p><h1>{{ route.meta.title }}</h1><p>{{ route.meta.description }}</p></div></div>
        <ErrorBanner :error="error" /><div v-if="verifying" class="panel state-box" role="status"><span class="spinner"></span>正在验证用户会话…</div><div v-else-if="error" class="panel state-box"><button class="button secondary" @click="verify">重新验证</button><button class="button text-button" @click="clearUserSession">清除本地用户会话</button></div><RouterView v-else :key="userSession?.access_token" />
        <footer class="page-footer"><span>Fcard · 用户空间</span><span>沙盒测试 · 暂未开放真实资金功能</span></footer>
      </main>
    </div>
  </div>
</template>
