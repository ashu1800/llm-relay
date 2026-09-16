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
      // 请求日志已并入数据看板（2026-09-16）。旧地址保留为跳转而不是删掉：
      // 排障时发出去的链接、浏览器书签、还有几个脚本都还指着 /console/logs，
      // 直接 404（落到 catch-all）会把 query 一起丢掉 ——
      // ?trace_id=… 和 ?status_class=error 正是那种链接里唯一有用的部分。
      {
        path: 'logs',
        redirect: (to) => ({ path: '/console/dashboard', query: to.query })
      },
      { path: 'keys', name: 'keys', component: () => import('@/views/KeysView.vue'), meta: { title: '密钥信息' } },
      { path: 'channels', name: 'channels', component: () => import('@/views/ChannelsView.vue'), meta: { title: '渠道管理' } },
      { path: 'groups', name: 'groups', component: () => import('@/views/GroupsView.vue'), meta: { title: '分组管理' } },
      { path: 'proxies', name: 'proxies', component: () => import('@/views/ProxiesView.vue'), meta: { title: '代理管理' } },
      { path: 'system', name: 'system', component: () => import('@/views/SettingsView.vue'), meta: { title: '系统设置' } }
    ]
  },
  { path: '/:pathMatch(.*)*', redirect: '/console/dashboard' }
]

export default createRouter({
  history: createWebHistory(),
  routes
})
