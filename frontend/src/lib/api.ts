import type { Admin, Audit, Envelope, LoginResult, Page, Status, User, UserInput } from '../types'
import { clearSession, token } from './session'
import { clearUserSession, userToken, requirePasswordChange } from './user-session'
import type { UserLoginResult, UserProfile } from '../types'
import type { CardAvailability, CardholderBinding, CardOperation, SandboxCard } from '../types'

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly code: string,
    public readonly requestID?: string,
  ) { super(message) }
}

const messages: Record<string, string> = {

  issuing_disabled: '沙盒开卡尚未配置，请联系管理员。',
  provider_unavailable: '供应商请求未完成，请核查状态，不要重复创建新申请。',
  cardholder_required: '请联系管理员配置公司统一沙盒发卡身份。',
  card_limit_reached: '沙盒每位用户最多一张已创建或结果待核查的卡片。',
  idempotency_conflict: '原申请编号已用于其他内容，请勿修改待核查申请。',
  password_change_required: '请先修改临时密码。',
  invalid_state: '当前账号状态不允许此操作。',
  invalid_input: '请检查输入内容是否符合要求。',
  unauthorized: '登录信息无效或会话已失效。',
  not_found: '该记录不存在或已不可访问。',
  conflict: '该邮箱已存在，请使用其他邮箱。',
  rate_limited: '登录尝试过于频繁，请一分钟后重试。',
  internal_error: '服务处理失败，请稍后重试。',
  not_ready: '数据库或迁移未就绪，请检查后端服务。',
}

interface Options {

  idempotencyKey?: string
  realm?: 'admin' | 'user'
  method?: 'GET' | 'POST' | 'PATCH'
  body?: unknown
  auth?: boolean
  signal?: AbortSignal
}

export async function request<T>(path: string, options: Options = {}): Promise<T> {
  const readToken = options.realm === 'user' ? userToken : token
  const clear = options.realm === 'user' ? clearUserSession : clearSession
  const accessToken = options.auth === false ? null : readToken()
  if (options.auth !== false && !accessToken) {
    throw new ApiError(messages.unauthorized!, 401, 'unauthorized')
  }
  const headers = new Headers({ Accept: 'application/json' })
  if (accessToken) headers.set('Authorization', `Bearer ${accessToken}`)
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')
  headers.set('X-Request-ID', crypto.randomUUID())
  if (options.idempotencyKey) headers.set('Idempotency-Key', options.idempotencyKey)
  let response: Response
  try {
    response = await fetch(path, {
      method: options.method ?? 'GET',
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: options.signal ? AbortSignal.any([options.signal, AbortSignal.timeout(15000)]) : AbortSignal.timeout(15000),
      cache: 'no-store',
      credentials: 'omit',
    })
  } catch (error) {
    if (options.signal?.aborted) throw error
    throw new ApiError('无法连接后端或请求超时，请检查服务是否启动。', 0, 'network_error')
  }
  let envelope: Envelope<T>
  if (response.status === 401 && options.auth !== false && readToken() === accessToken) clear()
  try {
    envelope = await response.json() as Envelope<T>
  } catch {
    throw new ApiError('后端未返回有效 JSON，请检查代理配置和服务状态。', response.status, 'invalid_response')
  }
  if (!envelope || typeof envelope !== 'object') {
    throw new ApiError('后端返回了无效的数据结构。', response.status, 'invalid_response')
  }
  if (!response.ok || envelope.error) {
    const code = envelope.error?.code ?? 'unknown_error'
    if (code === 'password_change_required' && options.realm === 'user' && accessToken) requirePasswordChange(accessToken)
    throw new ApiError(messages[code] ?? '请求失败，请稍后重试。', response.status, code, envelope.request_id)
  }
  if (envelope.data === undefined) throw new ApiError('响应缺少业务数据。', response.status, 'invalid_response', envelope.request_id)
  return envelope.data
}

const prefix = '/api/v1/admin'
export const api = {
  cardholder: (signal?: AbortSignal) => request<CardholderBinding>(`${prefix}/company/cardholder`, { signal }),
  bindCardholder: (cardholderID: string) => request<CardholderBinding>(`${prefix}/company/cardholder`, { method: 'POST', body: { provider_cardholder_id: cardholderID } }),
  cardOperation: (userID: string, operationID: string) => request<CardOperation>(`${prefix}/users/${userID}/card-operations/${operationID}`),
  reconcileCard: (userID: string, operationID: string, cardID: string) => request<CardOperation>(`${prefix}/users/${userID}/card-operations/${operationID}/reconcile`, { method: 'POST', body: { provider_card_id: cardID } }),
  login: (email: string, password: string) => request<LoginResult>(`${prefix}/auth/login`, { method: 'POST', body: { email, password }, auth: false }),
  me: () => request<Admin>(`${prefix}/auth/me`),
  logout: () => request<{ logged_out: boolean }>(`${prefix}/auth/logout`, { method: 'POST' }),
  users: (page = 1, pageSize = 10, signal?: AbortSignal) => request<Page<User>>(`${prefix}/users?page=${page}&page_size=${pageSize}`, { signal }),
  user: (id: string) => request<User>(`${prefix}/users/${encodeURIComponent(id)}`),
  createUser: (input: UserInput) => request<User>(`${prefix}/users`, { method: 'POST', body: input }),
  updateUser: (id: string, input: Partial<UserInput> & { status?: Status }) => request<User>(`${prefix}/users/${encodeURIComponent(id)}`, { method: 'PATCH', body: input }),
  resetUserPassword: (id: string, temporaryPassword: string) => request<{ login_enabled: boolean; must_change_password: boolean; sessions_revoked: boolean }>(`${prefix}/users/${encodeURIComponent(id)}/password/reset`, { method: 'POST', body: { temporary_password: temporaryPassword } }),
  audit: (page = 1, pageSize = 10, signal?: AbortSignal) => request<Page<Audit>>(`${prefix}/audit-logs?page=${page}&page_size=${pageSize}`, { signal }),
  health: (kind: 'live' | 'ready') => request<{ status: string }>(`/health/${kind}`, { auth: false }),
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '发生未知错误，请稍后重试。'
}

const userPrefix = '/api/v1/user/auth'
export const userApi = {
  cardConfig: (signal?: AbortSignal) => request<CardAvailability>('/api/v1/user/cards/config', { realm: 'user', signal }),
  cards: (page = 1, size = 20, signal?: AbortSignal) => request<Page<SandboxCard>>(`/api/v1/user/cards?page=${page}&page_size=${size}`, { realm: 'user', signal }),
  createCard: (nickname: string, key: string) => request<CardOperation>('/api/v1/user/cards', { method: 'POST', realm: 'user', idempotencyKey: key, body: { nick_name: nickname } }),
  card: (id: string) => request<SandboxCard>(`/api/v1/user/cards/${id}`, { realm: 'user' }),
  syncCard: (id: string) => request<SandboxCard>(`/api/v1/user/cards/${id}/sync`, { method: 'POST', realm: 'user' }),
  cardOperation: (id: string, signal?: AbortSignal) => request<CardOperation>(`/api/v1/user/card-operations/${id}`, { realm: 'user', signal }),
  retryCard: (id: string) => request<CardOperation>(`/api/v1/user/card-operations/${id}/retry`, { method: 'POST', realm: 'user' }),
  login: (email: string, password: string) => request<UserLoginResult>(`${userPrefix}/login`, { method: 'POST', body: { email, password }, auth: false, realm: 'user' }),
  me: (signal?: AbortSignal) => request<UserProfile>(`${userPrefix}/me`, { realm: 'user', signal }),
  logout: () => request<{ logged_out: boolean }>(`${userPrefix}/logout`, { method: 'POST', realm: 'user' }),
  changePassword: (currentPassword: string, newPassword: string) => request<{ password_changed: boolean; reauthenticate: boolean }>(`${userPrefix}/change-password`, { method: 'POST', realm: 'user', body: { current_password: currentPassword, new_password: newPassword } }),
}
