<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { History, RefreshCw, X, Fingerprint } from '@lucide/vue'
import { api } from '../lib/api'
import { actionNames, dateTime, shortID } from '../lib/format'
import type { Audit, Page } from '../types'
import ErrorBanner from '../components/ErrorBanner.vue'
import PaginationBar from '../components/PaginationBar.vue'

const records = ref<Page<Audit>>({ items: [], total: 0, page: 1, page_size: 10 })
const page = ref(1)
const pageSize = ref(10)
const loading = ref(false)
const error = ref<unknown>(null)
const selected = ref<Audit | null>(null)
const dialog = ref<HTMLDialogElement | null>(null)
let controller: AbortController | null = null

async function load(): Promise<void> {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = null
  try { const result = await api.audit(page.value, pageSize.value, current.signal); if (!current.signal.aborted) records.value = result }
  catch (failure) { if (!current.signal.aborted) error.value = failure }
  finally { if (!current.signal.aborted) loading.value = false }
}
function changePage(value: number): void { page.value = value; void load() }
function changeSize(value: number): void { pageSize.value = value; page.value = 1; void load() }
function open(event: Audit): void { selected.value = event; dialog.value?.showModal() }
function fields(event: Audit): string {
  const names: Record<string, string> = { email: '邮箱', display_name: '显示名称', status: '状态' }
  if (Array.isArray(event.changes.fields)) return event.changes.fields.map((field) => names[String(field)] ?? String(field)).join('、')
  return event.changes.source === 'local_cli' ? '本地维护命令' : '会话操作'
}
onMounted(load)
onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <div class="section-toolbar"><span class="muted">记录由后端生成 · 只读查看，不可修改</span><button class="button secondary compact" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{ rotating: loading }" />刷新记录</button></div>
  <ErrorBanner :error="error" />
  <section class="panel"><div class="panel-heading"><div class="heading-with-icon"><span class="metric-icon sand"><History :size="19" /></span><div><h2>操作记录 <span class="count-badge">{{ records.total }}</span></h2><p>按操作时间由新到旧排列</p></div></div><span class="tag">READ ONLY</span></div>
    <div v-if="loading" class="state-box" role="status"><span class="spinner"></span>正在加载审计记录…</div><div v-else-if="error" class="state-box"><strong>审计记录加载失败</strong><button class="button secondary compact" @click="load">重新加载</button></div><div v-else-if="records.items.length === 0" class="state-box"><span class="empty-icon"><History :size="27" /></span><strong>暂无操作记录</strong><p>登录及用户管理操作会在这里留下记录。</p></div>
    <div v-else class="table-scroll"><table><thead><tr><th>操作类型</th><th>操作对象</th><th>变更摘要</th><th>操作时间</th><th class="align-right">追踪</th></tr></thead><tbody><tr v-for="event in records.items" :key="event.id"><td><div class="audit-action"><span class="activity-icon"><History :size="15" /></span><div><strong>{{ actionNames[event.action] ?? event.action }}</strong><small class="mono">{{ event.action }}</small></div></div></td><td class="mono muted" :title="event.target_id">{{ shortID(event.target_id) }}</td><td>{{ fields(event) }}</td><td class="muted nowrap">{{ dateTime(event.created_at) }}</td><td><div class="row-actions"><button @click="open(event)"><Fingerprint :size="14" />详情</button></div></td></tr></tbody></table></div>
    <PaginationBar :page="page" :page-size="pageSize" :total="records.total" :loading="loading" @page="changePage" @size="changeSize" />
  </section>
  <p class="below-note"><Fingerprint :size="15" /> 请求编号可用于关联后端日志，变更摘要不包含密码或完整敏感资料。</p>
  <dialog ref="dialog" class="modal" aria-labelledby="audit-dialog-title"><div class="modal-heading"><div><p class="eyebrow">AUDIT TRAIL</p><h2 id="audit-dialog-title">操作追踪详情</h2></div><button class="icon-button" aria-label="关闭对话框" @click="dialog?.close()"><X :size="20" /></button></div><div v-if="selected" class="modal-body"><dl class="detail-list"><div><dt>操作类型</dt><dd>{{ actionNames[selected.action] ?? selected.action }}</dd></div><div><dt>记录编号</dt><dd class="mono">{{ selected.id }}</dd></div><div><dt>管理员编号</dt><dd class="mono">{{ selected.admin_id }}</dd></div><div><dt>对象编号</dt><dd class="mono">{{ selected.target_id }}</dd></div><div><dt>请求编号</dt><dd class="mono">{{ selected.request_id }}</dd></div><div><dt>操作时间</dt><dd>{{ dateTime(selected.created_at) }}</dd></div></dl><p class="eyebrow json-label">脱敏变更摘要</p><pre class="json-block">{{ JSON.stringify(selected.changes, null, 2) }}</pre></div><div class="modal-footer"><button class="button secondary" @click="dialog?.close()">关闭</button></div></dialog>
</template>
