<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ArrowUpRight, Users, History, Server, RefreshCw, ArrowRight, Check, CircleDashed } from '@lucide/vue'
import { api } from '../lib/api'
import { actionNames, dateTime } from '../lib/format'
import type { Audit, User } from '../types'
import ErrorBanner from '../components/ErrorBanner.vue'

const loading = ref(false)
const error = ref<unknown>(null)
const totalUsers = ref<number | null>(null)
const totalAudit = ref<number | null>(null)
const users = ref<User[]>([])
const audit = ref<Audit[]>([])
const live = ref(false)
const ready = ref(false)
const checkedAt = ref<string | null>(null)

async function load(): Promise<void> {
  if (loading.value) return
  loading.value = true
  error.value = null
  const results = await Promise.allSettled([api.users(1, 5), api.audit(1, 4), api.health('live'), api.health('ready')])
  const [userResult, auditResult, liveResult, readyResult] = results
  if (userResult?.status === 'fulfilled') { totalUsers.value = userResult.value.total; users.value = userResult.value.items }
  else { totalUsers.value = null; users.value = [] }
  if (auditResult?.status === 'fulfilled') { totalAudit.value = auditResult.value.total; audit.value = auditResult.value.items }
  else { totalAudit.value = null; audit.value = [] }
  live.value = liveResult?.status === 'fulfilled' && liveResult.value.status === 'ok'
  ready.value = readyResult?.status === 'fulfilled' && readyResult.value.status === 'ready'
  const failure = results.find((result) => result.status === 'rejected')
  if (failure?.status === 'rejected') error.value = failure.reason
  checkedAt.value = new Date().toISOString()
  loading.value = false
}
onMounted(load)
</script>

<template>
  <div class="section-toolbar"><div class="welcome-line"><span class="dot"></span> 从这里开始管理你的平台</div><button class="button secondary compact" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{ rotating: loading }" />刷新状态</button></div>
  <ErrorBanner :error="error" />
  <section class="metric-grid" aria-label="平台概况">
    <article class="metric-card"><div><span class="metric-label">用户总数</span><span class="metric-icon"><Users :size="20" /></span></div><strong>{{ loading ? '—' : totalUsers ?? '—' }}<small>位</small></strong><p>平台中已创建的测试用户</p></article>
    <article class="metric-card"><div><span class="metric-label">审计记录</span><span class="metric-icon sand"><History :size="20" /></span></div><strong>{{ loading ? '—' : totalAudit ?? '—' }}<small>条</small></strong><p>后端记录的管理操作</p></article>
    <article class="metric-card"><div><span class="metric-label">服务状态</span><span class="metric-icon blue"><Server :size="20" /></span></div><strong class="metric-status">{{ loading ? '检查中' : live && ready ? '运行正常' : '待检查' }}<span class="status-dot" :class="{ healthy: live && ready && !loading }"></span></strong><p>{{ checkedAt ? `最近检查 ${dateTime(checkedAt)}` : '等待后端返回状态' }}</p></article>
  </section>
  <div class="overview-grid">
    <section class="panel"><div class="panel-heading"><div><h2>最近创建的用户</h2><p>开始建立你的测试用户体系</p></div><RouterLink class="inline-link" to="/users">全部用户 <ArrowUpRight :size="16" /></RouterLink></div><div v-if="loading" class="state-box" role="status"><span class="spinner"></span>正在加载…</div><div v-else-if="users.length === 0" class="state-box"><span class="empty-icon"><Users :size="25" /></span><strong>{{ error ? '暂时无法获取用户' : '还没有测试用户' }}</strong><p>{{ error ? '请检查服务连接后刷新。' : '创建第一位用户，开始验证管理流程。' }}</p><RouterLink class="button secondary compact" to="/users">前往用户管理 <ArrowRight :size="15" /></RouterLink></div><div v-else class="table-scroll"><table><thead><tr><th>用户</th><th>状态</th><th>创建时间</th></tr></thead><tbody><tr v-for="person in users" :key="person.id"><td><div class="person-cell"><span class="avatar user-avatar">{{ person.display_name.charAt(0).toUpperCase() }}</span><div><strong>{{ person.display_name }}</strong><small>{{ person.email }}</small></div></div></td><td><span class="status-pill" :class="person.status === 'ACTIVE' ? 'active' : 'disabled'"><span></span>{{ person.status === 'ACTIVE' ? '正常' : '已停用' }}</span></td><td class="muted nowrap">{{ dateTime(person.created_at) }}</td></tr></tbody></table></div></section>
    <section class="panel setup-panel"><div class="panel-heading"><div><h2>当前工作空间</h2><p>已接入的功能与后续边界</p></div><span class="tag">SANDBOX</span></div><ul class="capability-list"><li><span class="check-circle"><Check :size="14" /></span><div><strong>管理员与用户会话</strong><small>独立身份校验与访问控制</small></div><span class="quiet-label">已接入</span></li><li><span class="check-circle"><Check :size="14" /></span><div><strong>用户管理与审计</strong><small>资料、状态和变更记录</small></div><span class="quiet-label">已接入</span></li><li><span class="check-circle"><Check :size="14" /></span><div><strong>沙盒虚拟卡申请</strong><small>需配置启用，并设置公司统一发卡身份</small></div><RouterLink class="inline-link" to="/cardholders">公司发卡配置</RouterLink></li><li><span class="pending-circle"><CircleDashed :size="18" /></span><div><strong>资金与消费记账</strong><small>后续 M2 开发，不可操作</small></div></li></ul><div class="health-list"><div><span>HTTP 服务</span><span :class="live ? 'text-green' : 'muted'">{{ loading ? '检查中' : live ? '可访问' : '未确认' }}</span></div><div><span>数据库与迁移</span><span :class="ready ? 'text-green' : 'muted'">{{ loading ? '检查中' : ready ? '已就绪' : '未确认' }}</span></div></div></section>
  </div>
  <section class="panel activity-panel"><div class="panel-heading"><div><h2>最近操作</h2><p>每一步管理操作，都有记录</p></div><RouterLink class="inline-link" to="/audit">查看审计 <ArrowUpRight :size="16" /></RouterLink></div><div v-if="!audit.length" class="subtle-empty">{{ loading ? '正在加载审计记录…' : '暂无可展示的审计记录' }}</div><div v-else class="activity-list"><div v-for="event in audit" :key="event.id" class="activity-item"><span class="activity-icon"><History :size="16" /></span><div><strong>{{ actionNames[event.action] ?? event.action }}</strong><small class="mono">{{ event.request_id }}</small></div><time>{{ dateTime(event.created_at) }}</time></div></div></section>
</template>
