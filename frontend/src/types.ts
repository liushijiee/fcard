export type Status = 'ACTIVE' | 'DISABLED'

export interface Admin {
  id: string
  email: string
  status: Status
  created_at: string
}

export interface LoginResult {
  access_token: string
  token_type: 'Bearer'
  expires_at: string
  admin: Admin
}

export interface User {
  id: string
  email: string
  display_name: string
  status: Status
  created_at: string
  updated_at: string
  login_enabled: boolean
  must_change_password: boolean
}

export interface UserProfile {
  id: string
  email: string
  display_name: string
  status: Status
  must_change_password: boolean
  created_at: string
}

export interface UserLoginResult {
  access_token: string
  token_type: 'Bearer'
  expires_at: string
  user: UserProfile
}

export interface UserInput {
  email: string
  display_name: string
}

export interface Audit {
  id: string
  admin_id: string
  action: string
  target_id: string
  request_id: string
  changes: Record<string, unknown>
  created_at: string
}

export interface Page<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

export interface Envelope<T> {
  data?: T
  error?: { code: string; message: string }
  request_id?: string
}

export interface CardholderBinding {
  bound: boolean
  provider_cardholder_id?: string
  legal_name?: string
}

export interface CardAvailability {
  enabled: boolean
  company_configured: boolean
  limit_cents: number
  currency: 'USD'
  max_cards: number
  occupied_slots: number
}

export interface SandboxCard {
  id: string
  nick_name: string
  status: string
  last_four: string
  currency: 'USD'
  limit_cents: number
  operation_id: string
  created_at: string
  updated_at: string
}

export interface CardOperation {
  id: string
  card_id: string
  status: 'PENDING' | 'PROCESSING' | 'SUCCEEDED' | 'FAILED' | 'UNKNOWN'
  error_code?: string
  attempts: number
  created_at: string
  updated_at: string
}
