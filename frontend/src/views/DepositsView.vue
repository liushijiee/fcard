<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { deposits, type DepositManagement, type ManagedDeposit } from '../lib/deposits'
import { token } from '../lib/session'
import ErrorBanner from '../components/ErrorBanner.vue'
import PaginationBar from '../components/PaginationBar.vue'

const records = ref<DepositManagement | null>(null)
const selected = ref<ManagedDeposit | null>(null)
const reason = ref('')
const reference = ref('')
const quote = ref('')
const page = ref(1)
const busy = ref(false)
const error = ref<unknown>(null)
const expected = token()
let disposed = false
let controller: AbortController | null = null
const current = () => !disposed && token() === expected
async function load(): Promise<void> {
  controller?.abort()
  const active = new AbortController()
  controller = active
  try { const result = await deposits.management(page.value, active.signal); if (current() && !active.signal.aborted) records.value = result }
  catch (failure) { if (current() && !active.signal.aborted) error.value = failure }
}
async function inspect(id: string): Promise<void> {
  try { const result = await deposits.detail(id); if (current()) { selected.value = result; quote.value = ''; reason.value = ''; reference.value = '' } }
  catch (failure) { if (current()) error.value = failure }
}
async function recover(): Promise<void> {
  if (!selected.value?.claim_token || busy.value || !confirm('确认已只读核对上游原交易证据与钱包？仅恢复原报价，不创建交易或确认资金。')) return
  busy.value = true
  error.value = null
  try {
    await deposits.recover(selected.value.order.id, { claim_token: selected.value.claim_token, reason: reason.value, evidence_reference: reference.value, quote: JSON.parse(quote.value) as unknown })
    if (current()) { selected.value = null; await load() }
  } catch (failure) { if (current()) error.value = failure }
  finally { if (current()) busy.value = false }
}
async function requeue(id: string): Promise<void> {
  const explanation = prompt('输入已核查的重排原因（至少8个字符）；仅重排核验，不重发 CREATE。')
  if (!explanation || busy.value) return
  busy.value = true
  try { await deposits.requeue(id, explanation); if (current()) await load() }
  catch (failure) { if (current()) error.value = failure }
  finally { if (current()) busy.value = false }
}
function changePage(value: number): void { page.value = value; void load() }
onMounted(load)
onUnmounted(() => { disposed = true; controller?.abort() })
</script>

<template>
  <ErrorBanner :error="error" /><p class="info-note">Nile 测试收款运维。未知创建会冻结整个审核范围，禁止清 claim 或重发 POST。恢复报价不确认资金；资金确认仅由已保存完整链证据驱动。</p>
  <section class="panel"><div class="panel-heading"><h2>收款与待审</h2><button class="button secondary" :disabled="busy" @click="load">刷新</button></div>
    <p v-if="records">未解决创建 {{ records.counts.UnresolvedCreates }} · 待审任务 {{ records.counts.ReviewJobs }} · 待处理任务 {{ records.counts.PendingJobs }}</p>
    <article v-for="order in records?.orders.items" :key="order.id" class="portal-card-item"><p>{{ order.id }} · {{ order.amount }} · {{ order.status }} · {{ order.review_reason }}</p><button :disabled="busy" @click="inspect(order.id)">查看审计与恢复</button></article>
    <PaginationBar v-if="records" :page="page" :page-size="20" :total="records.orders.total" :loading="busy" @page="changePage" />
  </section>
  <section v-if="selected" class="panel"><h2>订单审计</h2><p>上游原单号：{{ selected.gateway_order_id }}</p><p>claim：{{ selected.claim_token ?? '尚未发送' }}</p>
    <p v-for="event in selected.events" :key="event.ID">{{ event.CreatedAt }} · {{ event.Actor }} · {{ event.Action }}</p>
    <form v-if="selected.claim_token && ['CREATING', 'CREATE_UNKNOWN'].includes(selected.order.status)" @submit.prevent="recover">
      <p class="info-note">先以只读方式查阅 Epusdt 原始订单（包括实际金额、钱包与到期时间）；不得猜测报价。证据导出保存在受控位置，填写引用供后续审计。金额以字符串填写。</p>
      <label class="form-field">恢复原因<input v-model="reason" minlength="8" maxlength="512" required /></label><label class="form-field">证据引用<input v-model="reference" minlength="8" maxlength="512" required /></label><label class="form-field">原始报价 JSON<textarea v-model="quote" rows="8" maxlength="12000" required /></label><button class="button primary" :disabled="busy">审核恢复原报价（不发送 POST）</button>
    </form>
  </section>
  <section class="panel"><h2>待审任务（最近100条）</h2><article v-for="job in records?.review_jobs" :key="job.ID"><p>{{ job.Kind }} · {{ job.ID }} · {{ job.LastError }} · {{ job.Attempts }} 次</p><button v-if="job.Kind !== 'CREATE'" :disabled="busy" @click="requeue(job.ID)">受控重排核验</button></article></section>
  <section class="panel"><h2>尚无 receipt 归属的链事件（最近100条）</h2><p class="info-note">包含待处理、错金额、超时及重复付款；没有自动归属按钮，不代表可用余额。</p><p v-for="transfer in records?.unmatched_transfers" :key="`${transfer.TxHash}/${transfer.EventIndex}`" class="mono">{{ transfer.TxHash }}/{{ transfer.EventIndex }} · {{ transfer.ToAddress }} · 原始单位 {{ transfer.RawUnits }}</p></section>
</template>

<style scoped>
.panel { margin-bottom: 20px; padding: 20px; }
.mono { overflow-wrap: anywhere; }
textarea { width: 100%; resize: vertical; font: inherit; padding: 10px; border: 1px solid #dfe6d8; border-radius: 6px; }
</style>
