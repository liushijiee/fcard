<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { userApi } from '../lib/api'
import { clearUserSession, userSession, userToken } from '../lib/user-session'
import ErrorBanner from '../components/ErrorBanner.vue'

const router = useRouter()
const current = ref('')
const next = ref('')
const confirmation = ref('')
const saving = ref(false)
const error = ref<unknown>(null)
let active = true
function clean(): void { current.value = ''; next.value = ''; confirmation.value = '' }
onBeforeUnmount(() => { active = false; clean() })

async function change(): Promise<void> {
  if (saving.value) return
  error.value = null
  const length = new TextEncoder().encode(next.value).length
  if (length < 12 || length > 72 || next.value !== confirmation.value || current.value === next.value) {
    error.value = new Error('新密码须为 12–72 字节，与当前密码不同，且两次输入一致。')
    return
  }
  const expected = userToken()
  saving.value = true
  try {
    await userApi.changePassword(current.value, next.value)
    if (active && userToken() === expected) {
      clearUserSession()
      await router.replace({ name: 'portal-login', query: { changed: '1' } })
    }
  } catch (failure) { if (active) error.value = failure }
  finally { clean(); saving.value = false }
}
</script>

<template>
  <section class="panel portal-security">
    <div class="panel-heading"><div><h2>{{ userSession?.user.must_change_password ? '请先修改临时密码' : '修改登录密码' }}</h2><p>修改成功后，请使用新密码重新登录。</p></div></div>
    <div class="portal-details">
      <div v-if="userSession?.user.must_change_password" class="info-note">当前会话仅允许查看身份、修改密码及退出。完成改密后才能进入个人账号。</div>
      <ErrorBanner :error="error" />
      <form @submit.prevent="change">
        <label class="form-field">当前密码<input v-model="current" type="password" autocomplete="current-password" required :disabled="saving" /></label>
        <label class="form-field">新密码<input v-model="next" type="password" autocomplete="new-password" aria-label="新密码" aria-describedby="new-password-hint" required :disabled="saving" /><small id="new-password-hint" class="muted">12–72 字节；不能与当前密码相同。</small></label>
        <label class="form-field">确认新密码<input v-model="confirmation" type="password" autocomplete="new-password" required :disabled="saving" /></label>
        <button class="button primary" type="submit" :disabled="saving"><span v-if="saving" class="spinner"></span>{{ saving ? '保存中…' : '修改密码并重新登录' }}</button>
      </form>
    </div>
  </section>
</template>
