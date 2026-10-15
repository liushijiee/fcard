<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { deposits, payableDeposit, validDepositAmount, type DepositConfig, type DepositOrder } from '../lib/deposits'
import { userSession, userToken } from '../lib/user-session'
import { ApiError } from '../lib/api'
import type { Page } from '../types'
import { dateTime } from '../lib/format'
import ErrorBanner from '../components/ErrorBanner.vue'
import PaginationBar from '../components/PaginationBar.vue'

const config = ref<DepositConfig | null>(null)
const records = ref<Page<DepositOrder>>({ items: [], total: 0, page: 1, page_size: 20 })
const amount = ref('0.1')
const page = ref(1)
const now = ref(Date.now())
const clock = setInterval(() => { now.value = Date.now() }, 1000)
const busy = ref(false)
const loading = ref(false)
const error = ref<unknown>(null)
const submission = ref<{ key: string; amount: string } | null>(null)
const expected = userToken()
const storageKey = `fcard.user.deposit-create.${userSession.value?.user.id}`
let disposed = false
let controller: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | null = null
const current = () => !disposed && userToken() === expected
try {
  const saved: unknown = JSON.parse(sessionStorage.getItem(storageKey) ?? 'null')
  if (saved && typeof saved === 'object' && 'key' in saved && 'amount' in saved && typeof saved.key === 'string' && /^[0-9a-f-]{36}$/i.test(saved.key) && typeof saved.amount === 'string' && validDepositAmount(saved.amount)) {
    submission.value = { key: saved.key, amount: saved.amount }
    amount.value = saved.amount
  }
} catch { }
const canCreate = computed(() => config.value?.enabled && !busy.value && !loading.value && validDepositAmount(amount.value))

async function load(): Promise<void> {
  if (timer) clearTimeout(timer)
  controller?.abort()
  const active = new AbortController()
  controller = active
  loading.value = true
  try {
    const settings = await deposits.config(active.signal)
    if (!current() || active.signal.aborted) return
    config.value = settings
    if (!settings.available) return
    const orders = await deposits.list(page.value, active.signal)
    if (!current() || active.signal.aborted) return
    config.value = settings
    records.value = orders
    if (orders.items.some(order => ['CREATING', 'WAITING', 'VERIFYING'].includes(order.status))) timer = setTimeout(() => { void load() }, 3000)
  } catch (failure) { if (current() && !active.signal.aborted) error.value = failure }
  finally { if (current() && !active.signal.aborted) loading.value = false }
}
async function create(): Promise<void> {
  if (!canCreate.value) return
  error.value = null
  if (!submission.value) {
    const input = { key: crypto.randomUUID(), amount: amount.value }
    try { sessionStorage.setItem(storageKey, JSON.stringify(input)) }
    catch { error.value = new Error('无法保存订单编号，请允许会话存储后再提交。'); return }
    submission.value = input
  }
  busy.value = true
  try {
    await deposits.create(submission.value.amount, submission.value.key)
    if (!current()) return
    sessionStorage.removeItem(storageKey)
    submission.value = null
    await load()
  } catch (failure) {
    if (current()) {
      error.value = failure
      if (failure instanceof ApiError && ['invalid_input', 'idempotency_conflict'].includes(failure.code)) {
        sessionStorage.removeItem(storageKey)
        submission.value = null
      }
    }
  } finally { if (current()) busy.value = false }
}
function changePage(value: number): void { page.value = value; void load() }
onMounted(load)
onUnmounted(() => { disposed = true; clearInterval(clock); controller?.abort(); if (timer) clearTimeout(timer) })
</script>

<template>
  <ErrorBanner :error="error" />
  <p class="info-note">仅限 Nile 测试网的审核测试代币，不是主网 USDT。确认仅记录链上收款事实，不增加余额或卡额度；不提供提现。不要转入真实资产。</p>
  <p v-if="config?.max_amount" class="info-note">单笔申请上限 {{ config.max_amount }} 测试代币；请按订单返回的实际金额付款。</p>
  <section class="panel portal-card-form">
    <div class="panel-heading"><h2>创建测试收款订单</h2><button class="button secondary" :disabled="busy || loading" @click="load">刷新</button></div>
    <p v-if="!config?.enabled" class="info-note">收款未启用或扫描器尚未就绪。</p>
    <p v-if="submission" class="info-note">上次提交结果未确认，只能使用原金额与原编号重试 Fcard 请求；不会重发已 claim 的网关创建。</p>
    <form @submit.prevent="create"><label class="form-field">测试金额（最多六位小数）<input v-model="amount" inputmode="decimal" required :disabled="busy || !!submission" /></label><button class="button primary" :disabled="!canCreate">{{ busy ? '提交中' : submission ? '查询原提交结果' : '创建测试订单' }}</button></form>
  </section>
  <section class="panel portal-note">
    <h2>本人收款订单</h2><p v-if="!records.items.length" class="state-box">暂无订单</p>
    <article v-for="order in records.items" :key="order.id" class="portal-card-item">
      <h3>{{ order.amount }} 测试 USDT <span class="tag">{{ order.status }}</span></h3><p class="mono">订单：{{ order.id }}</p>
      <template v-if="payableDeposit(order, now)"><p>请在 {{ dateTime(order.expires_at!) }} 前精确转入 <strong>{{ order.actual_amount }}</strong>，不要使用申请金额代替实际金额。</p><p class="mono">Nile 收款地址：{{ order.receive_address }}</p><a v-if="order.payment_url?.startsWith('https://')" class="button secondary" :href="order.payment_url" target="_blank" rel="noopener noreferrer">打开审核来源收银台</a></template>
      <p v-else-if="order.status === 'CONFIRMED'" role="status">链上收款已确认；未增加余额或卡额度。</p>
      <p v-else-if="['CREATE_UNKNOWN', 'REVIEW_REQUIRED'].includes(order.status)">请联系管理员核查，勿再次转账或创建替代订单。{{ order.review_reason }}</p>
      <p v-else-if="order.status === 'EXPIRED'">订单已到期，请勿继续转账。窗口内已付款证据仍会核验。</p>
      <p v-else>等待后台核查；未出现有效报价前请勿转账。</p>
    </article>
    <PaginationBar :page="page" :page-size="20" :total="records.total" :loading="loading" @page="changePage" />
  </section>
</template>

<style scoped>
.mono { overflow-wrap: anywhere; }
</style>
