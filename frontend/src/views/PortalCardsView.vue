<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, userApi } from '../lib/api'
import { userSession, userToken } from '../lib/user-session'
import { dateTime } from '../lib/format'
import type { CardAvailability, CardOperation, Page, SandboxCard } from '../types'
import { ArrowDownUp, CreditCard, ShieldCheck, ArrowDown } from '@lucide/vue'
import PortalCardArtwork from '../components/PortalCardArtwork.vue'
import ErrorBanner from '../components/ErrorBanner.vue'
import PaginationBar from '../components/PaginationBar.vue'

const config = ref<CardAvailability | null>(null)
const records = ref<Page<SandboxCard>>({ items: [], total: 0, page: 1, page_size: 20 })
const operations = ref<Record<string, CardOperation>>({})
const nickname = ref('我的沙盒卡')
const loading = ref(false)
const busy = ref(false)
const error = ref<unknown>(null)
const notice = ref('')
const page = ref(1)
const pageSize = ref(20)
const storageKey = `fcard.user.card-create.${userSession.value?.user.id}`
const submission = ref<{ key: string; nickname: string } | null>(null)
const expectedToken = userToken()
let disposed = false
let controller: AbortController | null = null
let polling: ReturnType<typeof setTimeout> | null = null
try {
  const saved: unknown = JSON.parse(sessionStorage.getItem(storageKey) ?? 'null')
  if (saved && typeof saved === 'object' && 'key' in saved && 'nickname' in saved && typeof saved.key === 'string' && /^[0-9a-f-]{36}$/i.test(saved.key) && typeof saved.nickname === 'string') {
    submission.value = { key: saved.key, nickname: saved.nickname }
    nickname.value = saved.nickname
  }
} catch { }
const canCreate = computed(() => config.value?.enabled && config.value.company_configured && !loading.value && !busy.value && (submission.value || config.value.occupied_slots < config.value.max_cards))
const statusNames: Record<string, string> = { REQUESTED: '已提交申请', PENDING: '供应商处理中', PROCESSING: '请求处理中', SUCCEEDED: '开卡请求已完成', FAILED: '失败', UNKNOWN: '结果待核查', ACTIVE: '已激活', INACTIVE: '未激活', CLOSED: '已关闭', BLOCKED: '已冻结', EXPIRED: '已过期', LOST: '已挂失', STOLEN: '被盗停用' }
const amount = (cents: number) => new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'USD' }).format(cents / 100)
const current = () => !disposed && userToken() === expectedToken

async function load(): Promise<void> {
  if (polling) clearTimeout(polling)
  controller?.abort()
  const active = new AbortController()
  controller = active
  loading.value = true
  error.value = null
  try {
    const [settings, cards] = await Promise.all([userApi.cardConfig(active.signal), userApi.cards(page.value, pageSize.value, active.signal)])
    const statuses = await Promise.all(cards.items.map(item => userApi.cardOperation(item.operation_id, active.signal)))
    if (!current() || active.signal.aborted) return
    config.value = settings
    records.value = cards
    operations.value = Object.fromEntries(statuses.map(item => [item.id, item]))
    if (statuses.some(item => item.status === 'PENDING' || item.status === 'PROCESSING')) polling = setTimeout(() => { void load() }, 3000)
  } catch (failure) { if (current() && !active.signal.aborted) error.value = failure }
  finally { if (current() && !active.signal.aborted) loading.value = false }
}
async function create(): Promise<void> {
  if (!canCreate.value) return
  error.value = null
  if (!submission.value) {
    submission.value = { key: crypto.randomUUID(), nickname: nickname.value.trim() }
    try { sessionStorage.setItem(storageKey, JSON.stringify(submission.value)) }
    catch { error.value = new Error('无法保存申请编号，请允许当前标签页会话存储后再开卡。'); submission.value = null; return }
  }
  busy.value = true
  try {
    await userApi.createCard(submission.value.nickname, submission.value.key)
    if (!current()) return
    sessionStorage.removeItem(storageKey)
    submission.value = null
    notice.value = '申请已保存。后台将调用 Airwallex，页面会查询操作结果。'
    await load()
  } catch (failure) {
    if (current()) {
      error.value = failure
      if (failure instanceof ApiError && ['invalid_input', 'password_change_required', 'idempotency_conflict', 'card_limit_reached', 'cardholder_required', 'issuing_disabled'].includes(failure.code)) { sessionStorage.removeItem(storageKey); submission.value = null }
    }
  } finally { if (current()) busy.value = false }
}
async function retry(operation: CardOperation): Promise<void> {
  if (busy.value || !confirm('仅重放已保存的同一申请，不创建新卡请求。确认核查重试？')) return
  busy.value = true
  error.value = null
  try { await userApi.retryCard(operation.id); if (current()) await load() }
  catch (failure) { if (current()) error.value = failure }
  finally { if (current()) busy.value = false }
}
async function sync(card: SandboxCard): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = null
  try { await userApi.syncCard(card.id); if (current()) await load() }
  catch (failure) { if (current()) error.value = failure }
  finally { if (current()) busy.value = false }
}
function changePage(value: number): void { page.value = value; void load() }
function changeSize(value: number): void { pageSize.value = value; page.value = 1; void load() }
onMounted(load)
onUnmounted(() => { disposed = true; controller?.abort(); if (polling) clearTimeout(polling) })
</script>

<template>
  <div class="cards-experience">
  <section class="cards-hero"><div class="cards-hero-copy"><span class="mini-eyebrow">FCARD · VIRTUAL CARD</span><h1>让支付，多一种可能</h1><p>从一张虚拟卡开始，探索 Fcard 支付体验</p><a class="button hero-cta" href="#card-application">申请我的卡片 <ArrowDown :size="16" /></a><small>沙盒虚拟卡 · 在线申请与状态管理</small></div><PortalCardArtwork /></section>
  <div class="card-benefits"><article><span class="benefit-icon blue"><ArrowDownUp :size="25" /></span><h2>额度清晰可见</h2><p>查看 USD 累计测试限额</p><small>限额与钱包余额分别展示</small></article><article><span class="benefit-icon mint"><CreditCard :size="25" /></span><h2>轻松管理卡片</h2><p>申请进度与卡片状态一目了然</p><small>支持同步最新卡片状态</small></article><article><span class="benefit-icon violet"><ShieldCheck :size="25" /></span><h2>安心查看，隐私保护</h2><p>仅展示卡片尾号与状态</p><small>完整卡号与 CVV 不在此展示</small></article></div>
  <ErrorBanner :error="error" />
  <div class="info-note">新申请为沙盒企业虚拟卡（非个性化），统一使用公司现有发卡身份。限额是供应商消费控制，不是用户余额；未实现入金、消费记账或真实资金服务。账号停用不等于供应商卡片冻结。</div>
  <p v-if="notice" role="status" class="portal-note">{{ notice }}</p>
  <section id="card-application" class="panel portal-card-form">
    <div class="panel-heading"><div><h2>申请虚拟卡</h2><p v-if="config">每位用户最多 {{ config.max_cards }} 张未关闭或待核查卡片；当前占用 {{ config.occupied_slots }} 个名额，USD 累计测试限额 {{ amount(config.limit_cents) }}。</p></div><button class="button secondary compact" :disabled="loading || busy" @click="load">刷新本地状态</button></div>
    <p v-if="config && !config.enabled" class="info-note">管理员尚未启用沙盒开卡。</p>
    <p v-else-if="config && !config.company_configured" class="info-note">请联系管理员配置公司统一发卡身份，客户无需单独绑定持卡人。</p>
    <p v-if="config" class="info-note">在 Airwallex 注销旧卡后，请同步供应商状态。确认已关闭后可重新申请，历史记录保留。</p>
    <p v-if="submission" class="info-note">上次申请结果尚未确认。请保持原内容，用原编号重试，不要创建新申请。</p>
    <form @submit.prevent="create"><label class="form-field">卡片名称<input v-model="nickname" maxlength="100" required :disabled="busy || Boolean(submission)" /></label><button class="button primary" :disabled="!canCreate">{{ busy ? '处理中…' : submission ? '重试原申请' : '申请沙盒虚拟卡' }}</button></form>
  </section>
  <section class="panel portal-note"><div class="panel-heading"><div><h2>本人卡片</h2><p>仅展示状态和尾号，不展示完整卡号或 CVV。</p></div></div>
    <p v-if="!records.items.length" class="state-box">{{ loading ? '正在加载…' : '暂无卡片申请' }}</p>
    <article v-for="card in records.items" :key="card.id" class="portal-card-item">
      <h3>{{ card.nick_name }} <span class="tag">{{ statusNames[card.status] ?? card.status }}</span></h3>
      <p>卡尾号：{{ card.last_four ? `•••• ${card.last_four}` : '尚未返回' }} · 累计测试限额：{{ amount(card.limit_cents) }}</p>
      <p v-if="operations[card.operation_id]">开卡操作：{{ statusNames[operations[card.operation_id]!.status] }} <span v-if="operations[card.operation_id]!.error_code">（{{ operations[card.operation_id]!.error_code }}）</span></p>
      <p class="muted">更新于 {{ dateTime(card.updated_at) }}</p><p class="mono">操作编号：{{ card.operation_id }}</p>
      <div class="row-actions"><button v-if="operations[card.operation_id]?.status === 'SUCCEEDED'" :disabled="busy || !config?.enabled" @click="sync(card)">同步供应商状态</button><button v-if="operations[card.operation_id]?.status === 'UNKNOWN' && operations[card.operation_id]!.attempts < 5" :disabled="busy || !config?.enabled" @click="retry(operations[card.operation_id]!)">按原请求核查重试</button></div>
      <p v-if="operations[card.operation_id]?.status === 'UNKNOWN'" class="info-note">不能确认是否已开卡，不允许另开新卡。重试仍无法确认时，请把操作编号交给管理员核查。</p>
    </article>
    <PaginationBar :page="page" :page-size="pageSize" :total="records.total" :loading="loading" @page="changePage" @size="changeSize" />
  </section>
  <div class="cards-provider"><span class="mini-eyebrow">CARD INFRASTRUCTURE</span><h2>Airwallex</h2><p>当前已接入 Airwallex 沙盒发卡服务</p></div>
  </div>
</template>
