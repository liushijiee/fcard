<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { Plus, RefreshCw, Users, X, ArrowUpRight, ShieldCheck, CheckCircle2 } from '@lucide/vue'
import { api } from '../lib/api'
import { dateTime, shortID } from '../lib/format'
import type { Page, User } from '../types'
import ErrorBanner from '../components/ErrorBanner.vue'
import PaginationBar from '../components/PaginationBar.vue'

const records = ref<Page<User>>({ items: [], total: 0, page: 1, page_size: 10 })
const page = ref(1)
const pageSize = ref(10)
const loading = ref(false)
const error = ref<unknown>(null)
const notice = ref('')
let controller: AbortController | null = null
const dialog = ref<HTMLDialogElement | null>(null)
let dialogVersion = 0
const mode = ref<'create' | 'edit' | 'detail' | 'status' | 'password'>('create')
const selected = ref<User | null>(null)
const form = reactive({ email: '', display_name: '' })
const temporaryPassword = ref('')
const passwordConfirmation = ref('')
const dialogError = ref<unknown>(null)
const dialogLoading = ref(false)
const saving = ref(false)
const busy = computed(() => dialogLoading.value || saving.value)
const title = computed(() => ({ create: '创建测试用户', edit: '编辑用户资料', detail: '用户详情', password: selected.value?.login_enabled ? '重置登录密码' : '开通用户登录', status: selected.value?.status === 'ACTIVE' ? '停用用户' : '启用用户' })[mode.value])

async function load(): Promise<void> {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = null
  try {
    const result = await api.users(page.value, pageSize.value, current.signal)
    if (!current.signal.aborted) records.value = result
  } catch (failure) { if (!current.signal.aborted) error.value = failure }
  finally { if (!current.signal.aborted) loading.value = false }
}

function changePage(value: number): void { page.value = value; void load() }
function changeSize(value: number): void { pageSize.value = value; page.value = 1; void load() }
onMounted(load)
onBeforeUnmount(() => { controller?.abort(); dialogVersion++; clearPasswords() })

function clearPasswords(): void { temporaryPassword.value = ''; passwordConfirmation.value = '' }
function closed(): void { dialogVersion++; selected.value = null; clearPasswords() }

async function open(nextMode: typeof mode.value, person?: User): Promise<void> {
  const version = ++dialogVersion
  mode.value = nextMode
  clearPasswords()
  selected.value = null
  form.email = ''
  form.display_name = ''
  dialogError.value = null
  dialogLoading.value = Boolean(person)
  await nextTick()
  if (version !== dialogVersion) return
  dialog.value?.showModal()
  if (!person) return
  try {
    const result = await api.user(person.id)
    if (version !== dialogVersion || !dialog.value?.open) return
    selected.value = result
    form.email = selected.value.email
    form.display_name = selected.value.display_name
  } catch (failure) { if (version === dialogVersion) dialogError.value = failure }
  finally { if (version === dialogVersion) dialogLoading.value = false }
}

async function save(): Promise<void> {
  if (busy.value) return
  if (mode.value !== 'create' && !selected.value) return
  if (mode.value === 'password') {
    const length = new TextEncoder().encode(temporaryPassword.value).length
    if (length < 12 || length > 72 || temporaryPassword.value !== passwordConfirmation.value) {
      dialogError.value = new Error('临时密码须为 12–72 字节，且两次输入一致。')
      return
    }
  }
  saving.value = true
  dialogError.value = null
  try {
    if (mode.value === 'create') {
      await api.createUser({ email: form.email.trim(), display_name: form.display_name.trim() })
      page.value = 1
      notice.value = '测试用户已创建。'
    } else if (mode.value === 'edit' && selected.value) {
      await api.updateUser(selected.value.id, { email: form.email.trim(), display_name: form.display_name.trim() })
      notice.value = '用户资料已更新。'
    } else if (mode.value === 'password' && selected.value) {
      await api.resetUserPassword(selected.value.id, temporaryPassword.value)
      notice.value = '登录密码已设置。所有旧用户会话已撤销，请人工交付临时密码；用户首次登录必须改密。'
    } else if (mode.value === 'status' && selected.value) {
      await api.updateUser(selected.value.id, { status: selected.value.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE' })
      notice.value = selected.value.status === 'ACTIVE' ? '用户已停用，历史记录仍然保留。' : '用户已启用。'
    }
    dialog.value?.close()
    await load()
  } catch (failure) { dialogError.value = failure }
  finally { saving.value = false; clearPasswords() }
}

function cancel(event: Event): void { if (busy.value) event.preventDefault() }
</script>

<template>
  <div class="section-toolbar"><span class="muted">管理员创建用户 · 暂不开放自助注册</span><button class="button primary" @click="open('create')"><Plus :size="17" />创建用户</button></div>
  <div v-if="notice" class="success-banner" role="status"><CheckCircle2 :size="17" />{{ notice }}<button class="icon-button" aria-label="关闭成功提示" @click="notice = ''"><X :size="15" /></button></div>
  <ErrorBanner :error="error" />
  <section class="panel">
    <div class="panel-heading"><div class="heading-with-icon"><span class="metric-icon"><Users :size="19" /></span><div><h2>全部用户 <span class="count-badge">{{ records.total }}</span></h2><p>按创建时间由新到旧排列</p></div></div><button class="button secondary compact" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{ rotating: loading }" />刷新</button></div>
    <div v-if="loading" class="state-box" role="status"><span class="spinner"></span>正在加载用户…</div>
    <div v-else-if="error" class="state-box"><strong>用户列表加载失败</strong><p>数据未更新，请检查服务后重试。</p><button class="button secondary compact" @click="load">重新加载</button></div>
    <div v-else-if="records.items.length === 0" class="state-box"><span class="empty-icon"><Users :size="27" /></span><strong>还没有测试用户</strong><p>创建用户后，可在这里管理资料与账户状态。</p><button class="button primary compact" @click="open('create')"><Plus :size="16" />创建第一位用户</button></div>
    <div v-else class="table-scroll"><table><thead><tr><th>用户信息</th><th>用户编号</th><th>状态 / 登录</th><th>创建时间</th><th class="align-right">操作</th></tr></thead><tbody><tr v-for="person in records.items" :key="person.id"><td><div class="person-cell"><span class="avatar user-avatar">{{ person.display_name.charAt(0).toUpperCase() }}</span><div><strong>{{ person.display_name }}</strong><small>{{ person.email }}</small></div></div></td><td class="mono muted" :title="person.id">{{ shortID(person.id) }}</td><td><span class="status-pill" :class="person.status === 'ACTIVE' ? 'active' : 'disabled'"><span></span>{{ person.status === 'ACTIVE' ? '正常' : '已停用' }}</span><p class="muted">{{ !person.login_enabled ? '未开通登录' : person.must_change_password ? '待首次改密' : '已开通登录' }}</p></td><td class="nowrap muted">{{ dateTime(person.created_at) }}</td><td><div class="row-actions"><button @click="open('detail', person)">详情 <ArrowUpRight :size="13" /></button><button @click="open('edit', person)">编辑</button><button :disabled="person.status !== 'ACTIVE'" @click="open('password', person)">{{ person.login_enabled ? '重置密码' : '开通登录' }}</button><button :class="{ 'danger-text': person.status === 'ACTIVE' }" @click="open('status', person)">{{ person.status === 'ACTIVE' ? '停用' : '启用' }}</button></div></td></tr></tbody></table></div>
    <PaginationBar :page="page" :page-size="pageSize" :total="records.total" :loading="loading" @page="changePage" @size="changeSize" />
  </section>
  <p class="below-note"><ShieldCheck :size="15" /> 用户创建与资料变更会自动写入操作审计。停用不会删除用户，也不等于冻结未来关联的卡片。</p>

  <dialog ref="dialog" class="modal" aria-labelledby="user-dialog-title" @cancel="cancel" @close="closed">
    <div class="modal-heading"><div><p class="eyebrow">USER MANAGEMENT</p><h2 id="user-dialog-title">{{ title }}</h2></div><button class="icon-button" aria-label="关闭对话框" :disabled="busy" @click="dialog?.close()"><X :size="20" /></button></div>
    <div class="modal-body"><ErrorBanner :error="dialogError" /><div v-if="dialogLoading" class="state-box" role="status"><span class="spinner"></span>正在获取最新资料…</div>
      <form v-else-if="mode === 'create' || (mode === 'edit' && selected)" id="user-form" @submit.prevent="save"><p class="muted form-intro">{{ mode === 'create' ? '创建资料后，通过“开通登录”单独设置临时密码。' : '修改登录邮箱会撤销该用户全部会话。' }}</p><label class="form-field">显示名称<input v-model="form.display_name" required maxlength="100" placeholder="例如：测试用户" :disabled="saving" /></label><label class="form-field">邮箱地址<input v-model="form.email" type="email" required maxlength="254" placeholder="user@example.com" :disabled="saving" /></label></form>
      <form v-else-if="mode === 'password' && selected" id="password-form" @submit.prevent="save"><p class="muted">目标用户：{{ selected.email }}</p><div class="info-note">此操作撤销所有旧用户会话，首次登录必须修改临时密码。密码不会在保存后展示，请通过安全方式人工交付，仅用于沙盒。</div><label class="form-field">临时密码<input v-model="temporaryPassword" type="password" autocomplete="new-password" required :disabled="saving" /></label><label class="form-field">确认临时密码<input v-model="passwordConfirmation" type="password" autocomplete="new-password" required :disabled="saving" /></label></form>
      <dl v-else-if="mode === 'detail' && selected" class="detail-list"><div><dt>显示名称</dt><dd>{{ selected.display_name }}</dd></div><div><dt>邮箱</dt><dd>{{ selected.email }}</dd></div><div><dt>用户编号</dt><dd class="mono">{{ selected.id }}</dd></div><div><dt>状态</dt><dd><span class="status-pill" :class="selected.status === 'ACTIVE' ? 'active' : 'disabled'">{{ selected.status === 'ACTIVE' ? '正常' : '已停用' }}</span></dd></div><div><dt>创建时间</dt><dd>{{ dateTime(selected.created_at) }}</dd></div><div><dt>更新时间</dt><dd>{{ dateTime(selected.updated_at) }}</dd></div></dl>
      <div v-else-if="mode === 'status' && selected" class="confirm-content"><span class="avatar user-avatar">{{ selected.display_name.charAt(0).toUpperCase() }}</span><strong>{{ selected.display_name }}</strong><p>{{ selected.email }}</p><div class="info-note">{{ selected.status === 'ACTIVE' ? '确认停用此用户？资料与审计记录会保留。此操作不会自动冻结卡片。' : '确认将此用户状态恢复为正常？' }}</div></div>
    </div>
    <div class="modal-footer"><button class="button secondary" :disabled="busy" @click="dialog?.close()">{{ mode === 'detail' ? '关闭' : '取消' }}</button><button v-if="mode === 'create' || (mode === 'edit' && selected)" type="submit" form="user-form" class="button primary" :disabled="busy"><span v-if="saving" class="spinner"></span>{{ saving ? '保存中…' : mode === 'create' ? '确认创建' : '保存修改' }}</button><button v-else-if="mode === 'password' && selected" type="submit" form="password-form" class="button primary" :disabled="busy || selected.status !== 'ACTIVE'"><span v-if="saving" class="spinner"></span>{{ saving ? '保存中…' : '确认设置临时密码' }}</button><button v-else-if="mode === 'status' && selected" class="button" :class="selected.status === 'ACTIVE' ? 'danger' : 'primary'" :disabled="busy" @click="save"><span v-if="saving" class="spinner"></span>{{ saving ? '处理中…' : selected.status === 'ACTIVE' ? '确认停用' : '确认启用' }}</button></div>
  </dialog>
</template>
