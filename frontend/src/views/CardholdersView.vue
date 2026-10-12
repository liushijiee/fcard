<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from '../lib/api'
import type { CardholderBinding, CardOperation, Page, User } from '../types'
import ErrorBanner from '../components/ErrorBanner.vue'
import PaginationBar from '../components/PaginationBar.vue'

const users = ref<Page<User>>({ items: [], total: 0, page: 1, page_size: 20 })
const page = ref(1)
const pageSize = ref(20)
const selectedID = ref('')
const holderID = ref('')
const binding = ref<CardholderBinding | null>(null)
const operationID = ref('')
const providerCardID = ref('')
const operation = ref<CardOperation | null>(null)
const loading = ref(false)
const busy = ref(false)
const error = ref<unknown>(null)
let disposed = false
let controller: AbortController | null = null

async function loadUsers(): Promise<void> {
  loading.value = true
  error.value = null
  try { const result = await api.users(page.value, pageSize.value); if (!disposed) { users.value = result; selectedID.value = '' } }
  catch (failure) { if (!disposed) error.value = failure }
  finally { if (!disposed) loading.value = false }
}
watch(selectedID, () => {
  operation.value = null
  operationID.value = ''
  providerCardID.value = ''
  error.value = null
})
async function loadCompany(): Promise<void> {
  const active = new AbortController()
  controller = active
  try {
    const result = await api.cardholder(active.signal)
    if (!disposed) { binding.value = result; holderID.value = result.provider_cardholder_id ?? '' }
  } catch (failure) { if (!disposed && !active.signal.aborted) error.value = failure }
}
async function bind(): Promise<void> {
  if (busy.value || !binding.value || binding.value.bound) return
  busy.value = true
  error.value = null
  try { const result = await api.bindCardholder(holderID.value.trim()); if (!disposed) binding.value = result }
  catch (failure) { if (!disposed) error.value = failure }
  finally { if (!disposed) busy.value = false }
}
async function inspect(): Promise<void> {
  if (busy.value || !selectedID.value) return
  busy.value = true
  operation.value = null
  error.value = null
  try { const result = await api.cardOperation(selectedID.value, operationID.value.trim()); if (!disposed) operation.value = result }
  catch (failure) { if (!disposed) error.value = failure }
  finally { if (!disposed) busy.value = false }
}
async function reconcile(): Promise<void> {
  if (busy.value || !operation.value || operation.value.status !== 'UNKNOWN') return
  busy.value = true
  error.value = null
  try { const result = await api.reconcileCard(selectedID.value, operation.value.id, providerCardID.value.trim()); if (!disposed) operation.value = result }
  catch (failure) { if (!disposed) error.value = failure }
  finally { if (!disposed) busy.value = false }
}
function changePage(value: number): void { page.value = value; void loadUsers() }
function changeSize(value: number): void { pageSize.value = value; page.value = 1; void loadUsers() }
onMounted(() => { void loadCompany(); void loadUsers() })
onUnmounted(() => { disposed = true; controller?.abort() })
</script>

<template>
  <ErrorBanner :error="error" />
  <section class="panel portal-card-form"><div class="panel-heading"><div><h2>公司统一发卡身份</h2><p>一次性配置公司现有 INDIVIDUAL 发卡身份；暂不拦截非 READY 状态，实际能否发卡以 Airwallex 响应为准。所有客户共用此配置，卡片归属由 Fcard 独立记录。</p></div></div>
    <div class="info-note">请填写公司账户现有的 Airwallex 持卡人 ID，而非客户身份。不会为客户创建持卡人或提交客户实名信息；本轮配置后不支持换绑。</div>
    <form v-if="binding" @submit.prevent="bind"><label class="form-field">Airwallex 持卡人 ID<input v-model="holderID" required pattern="[0-9a-fA-F-]{36}" :disabled="busy || binding.bound" /></label><p v-if="binding.bound" role="status">已配置：{{ binding.legal_name }}（{{ binding.provider_cardholder_id }}）</p><button v-else class="button primary" :disabled="busy">{{ busy ? '核验中…' : '核验并配置公司身份' }}</button></form>
  </section>
  <section class="panel portal-card-form portal-note"><div class="panel-heading"><div><h2>客户开卡结果核查</h2><p>选择客户后查询其操作；选择客户不会改变公司发卡身份。</p></div></div>
    <label class="form-field">选择测试用户<select v-model="selectedID" :disabled="loading || busy"><option value="">请选择当前页用户</option><option v-for="user in users.items" :key="user.id" :value="user.id">{{ user.display_name }} · {{ user.email }} · {{ user.status }}</option></select></label>
    <PaginationBar :page="page" :page-size="pageSize" :total="users.total" :loading="loading || busy" @page="changePage" @size="changeSize" />

  </section>
  <section v-if="selectedID" class="panel portal-card-form portal-note"><div class="panel-heading"><div><h2>核查结果未知的开卡操作</h2><p>从用户取得操作编号；在供应商找到已创建卡片后，后端核对持卡人、request_id 和全部平台 metadata。</p></div></div>
    <form @submit.prevent="inspect"><label class="form-field">开卡操作编号<input v-model="operationID" required pattern="[0-9a-fA-F-]{36}" :disabled="busy" /></label><button class="button secondary" :disabled="busy">查询操作</button></form>
    <p v-if="operation" role="status">操作状态：{{ operation.status }} · 尝试次数：{{ operation.attempts }}</p>
    <form v-if="operation?.status === 'UNKNOWN'" @submit.prevent="reconcile"><label class="form-field">已创建的 Airwallex 卡片 ID<input v-model="providerCardID" required pattern="[0-9a-fA-F-]{36}" :disabled="busy" /></label><button class="button primary" :disabled="busy">核验并补录结果</button></form>
  </section>
</template>
