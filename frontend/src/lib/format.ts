export function dateTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date)
}

export function shortID(value: string): string {
  return value.length > 16 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value
}

export const actionNames: Record<string, string> = {
  'company.issuing.configure': '配置公司发卡身份',
  'user.cardholder.bind': '绑定沙盒持卡人（历史）',
  'card.create.reconcile': '核查沙盒开卡结果',
  'admin.initialize': '初始化管理员',
  'admin.login': '管理员登录',
  'admin.logout': '管理员退出',
  'admin.disable': '停用管理员',
  'user.create': '创建用户',
  'user.update': '更新用户',
}
