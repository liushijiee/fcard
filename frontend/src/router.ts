import { createRouter, createWebHistory } from 'vue-router'
import { token } from './lib/session'
import AdminLayout from './components/AdminLayout.vue'
import PortalLayout from './components/PortalLayout.vue'
import { userSession, userToken } from './lib/user-session'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/portal/login', name: 'portal-login', component: () => import('./views/PortalLoginView.vue'), meta: { title: '用户登录' } },
    { path: '/portal', component: PortalLayout, meta: { requiresUser: true }, children: [
      { path: '', name: 'portal-home', component: () => import('./views/PortalHomeView.vue'), meta: { title: '个人账号', description: '查看自己的账号资料与访问状态。' } },
      { path: 'security', name: 'portal-security', component: () => import('./views/PortalSecurityView.vue'), meta: { title: '账号安全', description: '修改密码后，所有用户会话将失效。' } },
      { path: 'cards', name: 'portal-cards', component: () => import('./views/PortalCardsView.vue'), meta: { title: '我的卡片', description: '申请和查询本人沙盒虚拟卡；不提供真实资金或完整卡号。' } },
    ] },
    { path: '/login', name: 'login', component: () => import('./views/LoginView.vue') },
    {
      path: '/', component: AdminLayout, meta: { requiresAuth: true }, children: [
        { path: '', name: 'overview', component: () => import('./views/OverviewView.vue'), meta: { title: '工作台', description: '平台状态与最近操作，一目了然。' } },
        { path: 'users', name: 'users', component: () => import('./views/UsersView.vue'), meta: { title: '用户管理', description: '管理测试用户资料、账户状态与访问资格。' } },
        { path: 'audit', name: 'audit', component: () => import('./views/AuditView.vue'), meta: { title: '操作审计', description: '追踪每一次管理操作，保留完整的变更足迹。' } },
        { path: 'cardholders', name: 'cardholders', component: () => import('./views/CardholdersView.vue'), meta: { title: '公司发卡配置', description: '配置公司统一沙盒发卡身份，核查客户开卡结果。' } },
      ],
    },
    { path: '/:pathMatch(.*)*', component: () => import('./views/NotFoundView.vue') },
  ],
})

router.beforeEach((to) => {
  if (to.meta.requiresUser && !userToken()) return { name: 'portal-login' }
  if (to.meta.requiresUser && userSession.value?.user.must_change_password && to.name !== 'portal-security') return { name: 'portal-security' }
  if (to.name === 'portal-login' && userToken()) return { name: userSession.value?.user.must_change_password ? 'portal-security' : 'portal-home' }
  if (to.meta.requiresAuth && !token()) return { name: 'login' }
  if (to.name === 'login' && token()) return { name: 'overview' }
})

router.afterEach((to) => {
  document.title = `${String(to.meta.title ?? '管理控制台')} · Fcard`
})
