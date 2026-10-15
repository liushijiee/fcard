<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LayoutDashboard, Users, History, LogOut, Menu, X, ShieldCheck, ChevronRight, FlaskConical } from '@lucide/vue'
import { api } from '../lib/api'
import { clearSession, session, token, updateAdmin } from '../lib/session'
import ErrorBanner from './ErrorBanner.vue'

const route = useRoute()
const router = useRouter()
const menuOpen = ref(false)
const loggingOut = ref(false)
const error = ref<unknown>(null)
const verifying = ref(true)
const initial = computed(() => session.value?.admin.email.charAt(0).toUpperCase() ?? 'A')
const navigation = [
  { to: '/', label: '工作台', icon: LayoutDashboard },
  { to: '/users', label: '用户管理', icon: Users },
  { to: '/audit', label: '操作审计', icon: History },
  { to: '/cardholders', label: '公司发卡配置', icon: ShieldCheck },
  { to: '/deposits', label: '测试收款运维', icon: ShieldCheck },
]
const timer = window.setInterval(() => token(), 15000)
onUnmounted(() => window.clearInterval(timer))
watch(() => route.path, () => { menuOpen.value = false })
watch(session, (value) => { if (!value) void router.replace('/login') })

async function verify(): Promise<void> {
  verifying.value = true
  error.value = null
  try { updateAdmin(await api.me()) } catch (failure) { error.value = failure }
  finally { verifying.value = false }
}
onMounted(verify)

async function logout(): Promise<void> {
  loggingOut.value = true
  error.value = null
  try {
    await api.logout()
    clearSession()
    await router.replace('/login')
  } catch (failure) { error.value = failure }
  finally { loggingOut.value = false }
}
</script>

<template>
  <div class="admin-shell">
    <button v-if="menuOpen" class="sidebar-scrim" aria-label="关闭导航" @click="menuOpen = false"></button>
    <aside class="sidebar" :class="{ 'is-open': menuOpen }">
      <RouterLink to="/" class="brand" aria-label="Fcard 工作台"><span class="brand-mark">f<span></span></span><span>fcard<span class="brand-period">.</span></span></RouterLink>
      <div class="workspace"><span class="workspace-icon"><ShieldCheck :size="19" /></span><div><strong>管理工作空间</strong><small>Administrator workspace</small></div></div>
      <p class="nav-label">平台管理</p>
      <nav aria-label="主导航">
        <RouterLink v-for="item in navigation" :key="item.to" :to="item.to" :class="{ selected: route.path === item.to }"><component :is="item.icon" :size="19" /><span>{{ item.label }}</span><ChevronRight v-if="route.path === item.to" :size="15" class="nav-chevron" /></RouterLink>
      </nav>
      <div class="sidebar-bottom">
        <RouterLink to="/portal" class="button secondary compact">前往用户空间</RouterLink>
        <div class="sandbox-note"><FlaskConical :size="20" /><strong>沙盒工作空间</strong><p>仅用于测试用户与管理流程。<br />不处理真实资金。</p><span class="tag">M1.5 · 账号与访问层</span></div>
        <div class="admin-profile"><span class="avatar">{{ initial }}</span><div><strong>管理员</strong><small :title="session?.admin.email">{{ session?.admin.email }}</small></div><button class="icon-button" aria-label="退出登录" :disabled="loggingOut" @click="logout"><LogOut :size="18" /></button></div>
      </div>
    </aside>
    <div class="main-shell">
      <header class="topbar"><div class="breadcrumbs"><button class="icon-button mobile-menu" aria-label="展开导航" @click="menuOpen = !menuOpen"><X v-if="menuOpen" :size="20" /><Menu v-else :size="20" /></button><span>控制台</span><ChevronRight :size="14" /><strong>{{ route.meta.title }}</strong></div><div class="topbar-right"><span class="environment-badge"><span></span> SANDBOX</span><span class="avatar small">{{ initial }}</span></div></header>
      <main class="page-content">
        <div class="page-heading"><div><p class="eyebrow">FCARD ADMINISTRATION</p><h1>{{ route.meta.title }}</h1><p>{{ route.meta.description }}</p></div><span class="quiet-label">仅限管理员访问 <ShieldCheck :size="15" /></span></div>
        <ErrorBanner :error="error" />
        <div v-if="verifying" class="panel state-box" role="status"><span class="spinner"></span>正在验证管理员会话…</div>
        <div v-else-if="error" class="panel state-box"><p>会话校验或退出操作未完成，请重试。</p><button class="button secondary" @click="verify">重新验证</button><button class="button text-button" @click="clearSession()">仅清除本地会话</button></div>
        <RouterView v-else />
        <footer class="page-footer"><span>Fcard · 管理控制台</span><span>沙盒环境 / 所有操作可审计</span></footer>
      </main>
    </div>
  </div>
</template>
