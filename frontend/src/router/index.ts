import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

// 路由结构对齐参考站 /console/* 的命名，剔除登录/用户/订单/工单/兑换/礼品/邮件/公告
const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/console/dashboard' },
  {
    path: '/console',
    component: () => import('@/components/MainLayout.vue'),
    children: [
      { path: '', redirect: '/console/dashboard' },
      { path: 'dashboard', name: 'dashboard', component: () => import('@/views/DashboardView.vue'), meta: { title: '数据看板' } },
      { path: 'logs', name: 'logs', component: () => import('@/views/LogsView.vue'), meta: { title: '请求日志' } },
      { path: 'keys', name: 'keys', component: () => import('@/views/KeysView.vue'), meta: { title: '密钥信息' } },
      { path: 'channels', name: 'channels', component: () => import('@/views/ChannelsView.vue'), meta: { title: '渠道管理' } },
      { path: 'groups', name: 'groups', component: () => import('@/views/GroupsView.vue'), meta: { title: '分组管理' } },
      { path: 'channel-templates', name: 'channel-templates', component: () => import('@/views/TemplatesView.vue'), meta: { title: '模板管理' } },
      { path: 'models-manage', name: 'models-manage', component: () => import('@/views/ModelsView.vue'), meta: { title: '模型管理' } },
      { path: 'pricing', name: 'pricing', component: () => import('@/views/PricingView.vue'), meta: { title: '模型定价' } },
      { path: 'route-analysis', name: 'route-analysis', component: () => import('@/views/RoutingView.vue'), meta: { title: '路由分析' } },
      { path: 'system', name: 'system', component: () => import('@/views/SettingsView.vue'), meta: { title: '系统设置' } }
    ]
  },
  { path: '/:pathMatch(.*)*', redirect: '/console/dashboard' }
]

export default createRouter({
  history: createWebHistory(),
  routes
})
