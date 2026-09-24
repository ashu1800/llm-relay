import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { setOnUnauthorized } from '@/api/client'

// 路由结构对齐参考站 /console/* 的命名，剔除登录/用户/订单/工单/兑换/礼品/邮件/公告。
// 登录页在管理后台部署到公网后成为入口（无账号模型：只认管理密钥）。
const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/console/dashboard' },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    // 不进 MainLayout：登录页没有侧栏与菜单
    meta: { title: '登录' }
  },
  {
    path: '/console',
    component: () => import('@/components/MainLayout.vue'),
    children: [
      { path: '', redirect: '/console/dashboard' },
      // meta.fill：这一页要「填满一屏」—— 内容区因此拿到确定的高度，
      // 页面里那条「面板吃掉剩余高度」的弹性链才收得住，右侧容器底边才能
      // 与左侧栏最后一行落在同一条水平线上（做法与理由见 MainLayout 的 .content-inner.is-fill）
      {
        path: 'dashboard',
        name: 'dashboard',
        component: () => import('@/views/DashboardView.vue'),
        meta: { title: '数据看板', fill: true }
      },
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

const router = createRouter({
  history: createWebHistory(),
  routes
})

// ---- 登录守卫 ----
//
// 后端开启了登录鉴权（配置了 RELAY_ADMIN_KEY）时，未登录一律先去登录页；
// 未开启鉴权（旧部署、只绑回环）时 fetchStatus 报 enabled=false，全部放行，
// 行为与没有登录功能时完全一致。
//
// 状态只查一次（statusLoaded），之后由两条路径维持新鲜：
//   - 登录/登出各自更新 store；
//   - 任何接口 401（会话过期/被轮换）经 client.ts 的全局收口跳回登录页。
router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.statusLoaded) {
    try {
      await auth.fetchStatus()
    } catch {
      // status 拿不到（后端没起、网络断）：放行，让页面里的请求自己报错。
      // 在这里拦去登录页没有意义 —— 登录页同样打不到后端。
      return true
    }
  }
  if (auth.enabled && !auth.authenticated && to.path !== '/login') {
    // 带上完整目标地址（含 query）：登录后回到原来想去的地方，
    // 排障深链（?trace_id=…）才不会在登录这一步丢掉
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.path === '/login' && auth.authenticated) {
    return { path: '/console/dashboard' }
  }
})

// 会话过期的全局出口：client.ts 发现 401 时叫醒这里。
// 用 router.currentRoute 而不是闭包外部的 route 对象 —— 处理器注册时
// 还没有任何当前路由。
setOnUnauthorized(() => {
  const auth = useAuthStore()
  auth.markUnauthenticated()
  const current = router.currentRoute.value
  if (current.path === '/login') return
  router.push({ path: '/login', query: { redirect: current.fullPath } })
})

// ---- 页面标题（2026-09-24 UI 审评 P2-10）----
//
// 每个路由早就写了 meta.title，但全仓没有一处 document.title ——
// 于是 6 个管理页 + 登录页在标签栏上全都叫「LLM Relay」。多开几个标签排障时
// （看板一个、渠道一个、设置一个）分不清哪张是哪个页面，只能逐个点开看。
//
// 格式 `页面名 · LLM Relay`：**页面名在前**，因为标签栏是从右往左截断的 ——
// 把站点名放前面，多标签同开时看到的就是一排完全相同的「LLM Relay…」，
// 恰好丢掉唯一有区分度的那部分。
//
// 为什么写在 afterEach 而不是 beforeEach：守卫里可能把导航改道
//（未登录 → /login、已登录访问 /login → 看板），afterEach 拿到的才是
// **最终落地**的那条路由。写在 beforeEach 会先写上「数据看板」、
// 再跳登录页，标签闪一下错的标题。
//
// index.html 里那个 <title>LLM Relay</title> 保留：它是 JS 起来之前的兜底，
// 首屏加载期间标签上不至于空着。这里只负责 thereafter 的更新。
const APP_NAME = 'LLM Relay'
router.afterEach((to) => {
  const page = typeof to.meta.title === 'string' ? to.meta.title : ''
  document.title = page ? `${page} · ${APP_NAME}` : APP_NAME
})

export default router
