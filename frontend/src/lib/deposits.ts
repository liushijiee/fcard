import { request } from './api'
import type { Page } from '../types'

export interface DepositOrder {
  id: string
  user_id: string
  status: string
  environment: 'nile'
  network: 'tron'
  contract: string
  amount: string
  actual_amount?: string
  receive_address?: string
  payment_url?: string
  expires_at?: string
  review_reason?: string
  created_at: string
}
export interface DepositConfig { available: boolean; enabled: boolean; environment: string; balance_crediting: boolean; max_amount?: string }
export interface DepositManagement {
  orders: Page<DepositOrder>
  review_jobs: { ID: string; Kind: string; LastError: string; Attempts: number }[]
  unmatched_transfers: { TxHash: string; EventIndex: number; ToAddress: string; RawUnits: string; BlockTime: string }[]
  counts: { UnresolvedCreates: number; ReviewJobs: number; PendingJobs: number }
}
export interface ManagedDeposit {
  order: DepositOrder
  claim_token?: string
  gateway_order_id: string
  events: { ID: string; Action: string; Actor: string; CreatedAt: string }[]
}
export function validDepositAmount(value: string): boolean {
  if (!/^(0|[1-9][0-9]{0,6})(\.[0-9]{1,6})?$/.test(value)) return false
  const [whole, fraction = ''] = value.split('.')
  const units = BigInt(whole!) * 1000000n + BigInt(fraction.padEnd(6, '0'))
  return units > 10000n && units <= 1000000000000n
}
export function payableDeposit(order: DepositOrder, now = Date.now()): boolean {
  return order.status === 'WAITING' && !!order.receive_address && !!order.actual_amount && !!order.expires_at && Date.parse(order.expires_at) > now
}
export const deposits = {
  config: (signal?: AbortSignal) => request<DepositConfig>('/api/v1/user/deposits/config', { realm: 'user', signal }),
  list: (page = 1, signal?: AbortSignal) => request<Page<DepositOrder>>(`/api/v1/user/deposits?page=${page}&page_size=20`, { realm: 'user', signal }),
  create: (amount: string, key: string) => request<DepositOrder>('/api/v1/user/deposits', { realm: 'user', method: 'POST', idempotencyKey: key, body: { amount } }),
  management: (page = 1, signal?: AbortSignal) => request<DepositManagement>(`/api/v1/admin/deposits?page=${page}&page_size=20`, { signal }),
  detail: (id: string, signal?: AbortSignal) => request<ManagedDeposit>(`/api/v1/admin/deposits/${encodeURIComponent(id)}`, { signal }),
  recover: (id: string, input: { claim_token: string; reason: string; evidence_reference: string; quote: unknown }) => request<DepositOrder>(`/api/v1/admin/deposits/${encodeURIComponent(id)}/recover-quote`, { method: 'POST', body: input }),
  requeue: (id: string, reason: string) => request<{ requeued: boolean }>(`/api/v1/admin/deposit-jobs/${encodeURIComponent(id)}/requeue`, { method: 'POST', body: { reason } }),
}
