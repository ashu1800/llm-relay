<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '@/api/client'
import {
  DashboardOutlined,
  KeyOutlined,
  ApiOutlined,
  ClusterOutlined,
  GlobalOutlined,
  SettingOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  BulbOutlined,
  BulbFilled,
  LogoutOutlined
} from '@ant-design/icons-vue'
import { useThemeStore } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import { message } from 'ant-design-vue'
import { onLive } from '@/composables/useLive'
import { startTabPulse } from '@/utils/tabPulse'
import { moneyText } from '@/utils/money'
import VersionBadge from '@/components/VersionBadge.vue'

const route = useRoute()
const router = useRouter()
const themeStore = useThemeStore()
const authStore = useAuthStore()

// 退出登录：服务端清掉会话 Cookie，本地状态随之置否，回登录页。
// logout 内部已保证「请求失败也清本地状态」—— 会话可能早已过期。
async function logout() {
  await authStore.logout()
  router.push('/login')
}

// 主题切换带上点击坐标：新主题从灯泡位置圆形扩散铺满全屏
// （动画本身在 themeStore.toggleWithBurst，这里只负责把事件坐标递过去）
function toggleTheme(e: MouseEvent) {
  themeStore.toggleWithBurst(e.clientX, e.clientY)
}

// ---- 预算告警（全局）----
//
// 花费跨过分组日预算的 80% / 100% 档位时，后端经 live 推 budget_alert
// （每天每档一次，见后端 budget.go —— 只提醒不拦截是站主拍板的策略）。
// 放在常驻的布局层而不是某一页：预算烧穿这件事跟「用户此刻在哪个页面」
// 无关，任何页面都该第一时间知道。
type BudgetAlert = {
  group_id: number
  group_name: string
  currency: string
  level: '80' | '100'
  limit: number
  spent: number
  ratio: number
}

onLive('budget_alert', (a: BudgetAlert) => {
  // 金额走 utils/money 的统一规则（这里原来是 toFixed(2)，与日志的六位、
  // 分组的两位各说各话）。告警里两个数是给用户去日志里核对的，
  // 对不上就等于没给 —— 2026-09-24 UI 审评。
  const money = moneyText(a.spent, a.currency)
  const limitMoney = moneyText(a.limit, a.currency)
  // 超支不显示百分比：花费 36 倍于预算时「已达预算 3600%」除了吓人没有信息量，
  // 直接说「已超支」并给出两个数，用户自己看得懂
  const text =
    a.level === '100'
      ? `分组「${a.group_name}」今日${a.currency}消费已超支（${money} / 预算 ${limitMoney}）`
      : `分组「${a.group_name}」今日${a.currency}消费已达预算 ${Math.round(a.ratio * 100)}%（${money}）`
  if (a.level === '100') message.error(text + '，请注意控制用量')
  else message.warning(text)
})

// 默认加密密钥的提示。
//
// 用默认密钥时渠道密钥的加密等于没有：主密钥是从这个公开占位串推出来的，
// 数据库或备份文件落到别人手里就能直接解开。
// 原来只在启动日志里警告一句，容器日志一滚就看不见了 ——
// 这种事必须持续可见，所以放在界面上。
const usingDefaultSecret = ref(false)

// 登录鉴权未启用的提示（后端 /system/info 的 console_auth_enabled）。
// 服务绑在回环上时这是合理配置；一旦放到公网，管理接口等于对所有人敞开。
const authDisabled = ref(false)

// 后端版本号，显示在侧栏品牌下方。
//
// 它的用途很具体：**判断界面上看到的这个版本，是不是正在跑的那份代码**。
// README 里写过「改了前端或后端必须重新部署才会在 8888 上生效」，
// 但先前没有任何办法在界面上确认这件事 —— 改了代码、部署失败、还以为看到了新版。
//
// 版本号由构建时经 -ldflags 编进二进制（见 Dockerfile 与 install.sh），
// 所以它回的一定是真正跑着的那份，不是某个配置文件里的声明。
//
// 现在它由 VersionBadge 组件承担（见其注释）：那枚徽标除了显示版本，
// 还是「检测更新 / 一键更新 / 回滚」的入口。原来这里只是一个只读的
// <div class="brand-version">，两处显示同一个信息没有意义，所以直接替换。
//
// 取不到版本时整块不渲染：空着一格版本号比不显示更容易让人以为哪里坏了。

// 侧边栏菜单：对齐参考站 console-menu-list 的项目与顺序，
// 剔除其面向多用户的登录/工单/订单/兑换/礼品/邮件/公告模块。
//
// 「请求日志」不在菜单里：它已经并进数据看板（列表就在四张概览卡下面），
// 同一个页面在菜单里出现两次只会让人以为点错了。旧地址 /console/logs
// 仍然可用（见 router/index.ts 的跳转）。
const menus = [
  { key: '/console/dashboard', label: '数据看板', icon: DashboardOutlined },
  { key: '/console/keys', label: '密钥信息', icon: KeyOutlined },
  { key: '/console/channels', label: '渠道管理', icon: ApiOutlined },
  { key: '/console/groups', label: '分组管理', icon: ClusterOutlined },
  { key: '/console/proxies', label: '代理管理', icon: GlobalOutlined },
  { key: '/console/system', label: '系统设置', icon: SettingOutlined }
]

const collapsed = ref(false)
const isActive = (key: string) => route.path === key
const go = (key: string) => router.push(key)

// ---- 窄屏自动收起侧栏 ----
//
// 侧栏固定 192px。窗口一窄，它就要占掉一大半宽度，内容区被压成一条缝 ——
// 表格虽然有横向滚动（各页都配了 :scroll="{ x }"），但连「一屏能看见两列」
// 都做不到时，滚动也救不回来。
//
// 用 matchMedia 而不是纯 CSS：收起是**组件状态**（collapsed 同时决定
// 菜单文字、侧栏宽度、footer 排布），CSS 改不动它。两者混用还会打架。
//
// 跨过断点时跟随视口，但用户在同一档内手动展开/收起后不再被覆盖 ——
// 所以只在断点**变化**时写 collapsed，不在每次 resize 时写。
const NARROW = '(max-width: 900px)'
let narrowMq: MediaQueryList | null = null
const onNarrowChange = (e: MediaQueryListEvent | MediaQueryList) => {
  collapsed.value = e.matches
}

onMounted(() => {
  if (typeof window !== 'undefined' && window.matchMedia) {
    narrowMq = window.matchMedia(NARROW)
    // 首屏就按当前宽度定：窄屏进来时不该先闪一下展开态
    collapsed.value = narrowMq.matches
    narrowMq.addEventListener('change', onNarrowChange)
  }
  // 页签心跳：角标显示今日请求数，熔断/超支转红（数据走已有 live 推送）。
  // MainLayout 常驻，这里启动一次就覆盖整个会话
  startTabPulse()
  // 系统信息与窄屏初始化合在同一个 onMounted：原来有两个，各自请求一次
  // /system/info —— 每次进页面白打一个重复请求（窄屏适配改造时留下的）。
  //
  // 版本号曾经也搭这个请求顺路带回来。现在它由 VersionBadge 自己取
  // （见 stores/version.ts 的 fetchInfo）：那个组件需要一个结构化的
  // 版本对象（构建形态、是否 release、更新器是否在场），而 /system/info
  // 只给一个字符串。两处都取会让侧栏发两个请求，所以这里不再管版本。
  api
    .get<{ using_default_secret?: boolean; console_auth_enabled?: boolean }>('/system/info')
    .then((info) => {
      usingDefaultSecret.value = !!info.using_default_secret
      // 登录鉴权未启用时持续横幅提醒：只绑回环的旧部署不受影响，
      // 但公网部署下这就是把管理台裸奔给整个互联网
      authDisabled.value = info.console_auth_enabled === false
    })
    .catch(() => {
      // 拿不到系统信息不影响正常使用，静默即可
    })
})

onUnmounted(() => {
  narrowMq?.removeEventListener('change', onNarrowChange)
  narrowMq = null
})
</script>

<template>
  <div class="main-layout">
    <!-- 没有顶栏：品牌与主题切换都归到侧栏。
         参考站有一条 64px 的顶栏，但它装的是公告条和「控制台/模型/文档/头像」，
         我们这边只剩品牌一个元素 —— 留着就是一行空白，还把每个页面的内容
         整体压下 64px。品牌移到侧栏顶部、主题按钮移到侧栏底部之后，
         右侧内容直接顶到最上面。 -->
    <div class="main-layout-body">
      <div class="console-layout">
        <!-- 侧边栏：实测宽 192px，内边距 8px -->
        <aside class="console-sidebar" :class="{ 'is-collapsed': collapsed }">
          <div class="sidebar-top">
            <div class="brand" :title="collapsed ? 'LLM Relay' : ''" @click="go('/console/dashboard')">
              <span class="brand-mark">LR</span>
              <span v-if="!collapsed" class="brand-text">LLM Relay</span>
            </div>

            <!-- 版本号紧随品牌：它是这个应用的身份信息，与品牌同属一组。
                收起侧栏时不显示（那一列只有 40px 宽，塞不下）；
                窄屏下侧栏默认收起，所以它在窄屏是不可见的 —— 这是有意的，
                窄屏空间该留给内容，查版本可以用 deploy/install.sh --verify。 -->
            <VersionBadge v-if="!collapsed" class="brand-version-slot" />

            <!-- 品牌区（身份信息）与菜单区（导航）的分界。收起态不显示：
                 那时上下都是纯图标，一条线反而显得挤 -->
            <div v-if="!collapsed" class="sidebar-divider" aria-hidden="true"></div>

            <nav class="console-menu-list">
              <button
                v-for="m in menus"
                :key="m.key"
                class="console-menu-item"
                :class="{ active: isActive(m.key) }"
                :title="m.label"
                @click="go(m.key)"
              >
                <component :is="m.icon" class="console-menu-icon" />
                <span v-if="!collapsed" class="console-menu-label">{{ m.label }}</span>
              </button>
            </nav>
          </div>

          <div class="sidebar-footer">
            <!-- 收起时只剩一个图标，读屏会念成「menu-fold」这种图标名。
                 aria-label 给它一个真实的动作名；展开态有可见文字，
                 aria-label 与之一致即可，不会重复朗读。 -->
            <button
              class="console-menu-item"
              :aria-label="collapsed ? '展开侧栏' : '收起侧栏'"
              :aria-expanded="!collapsed"
              @click="collapsed = !collapsed"
            >
              <MenuUnfoldOutlined v-if="collapsed" class="console-menu-icon" aria-hidden="true" />
              <MenuFoldOutlined v-else class="console-menu-icon" aria-hidden="true" />
              <span v-if="!collapsed" class="console-menu-label">收起侧栏</span>
            </button>
            <!-- 退出登录（无账号模型：清掉会话 Cookie 回登录页）。
                 收起态同样只留图标，aria-label 保证动作可读 -->
            <button class="console-menu-item" aria-label="退出登录" @click="logout">
              <LogoutOutlined class="console-menu-icon" aria-hidden="true" />
              <span v-if="!collapsed" class="console-menu-label">退出登录</span>
            </button>
            <button
              class="nav-icon-btn"
              :title="themeStore.isDark ? '切换浅色' : '切换深色'"
              :aria-label="themeStore.isDark ? '切换到浅色主题' : '切换到深色主题'"
              :aria-pressed="themeStore.isDark"
              @click="toggleTheme"
            >
              <BulbOutlined v-if="!themeStore.isDark" aria-hidden="true" />
              <BulbFilled v-else aria-hidden="true" />
            </button>
          </div>
        </aside>

        <section class="console-content">
          <!-- is-fill 由路由的 meta.fill 给（目前只有数据看板）：这一页要填满一屏，
               内容区必须有确定高度，页面里那条弹性链才有「剩余多少」可分 -->
          <div class="content-inner" :class="{ 'is-fill': route.meta.fill }">
            <a-alert
              v-if="usingDefaultSecret"
              type="warning"
              show-icon
              banner
              class="secret-banner"
              message="正在使用默认加密密钥，渠道密钥的加密形同虚设"
            >
              <template #description>
                请设置环境变量 <code>RELAY_SECRET</code> 为一段随机字符串后重启服务。
                注意：更换密钥后，已保存的渠道密钥需要用原密钥重新加密，否则会解不开。
                <router-link to="/console/system">前往系统设置</router-link>
              </template>
            </a-alert>
            <a-alert
              v-if="authDisabled"
              type="error"
              show-icon
              banner
              class="secret-banner"
              message="管理后台登录鉴权未启用"
            >
              <template #description>
                未设置 <code>RELAY_ADMIN_KEY</code>，任何能访问本服务的人都可以打开管理台。
                仅绑定回环（127.0.0.1）的本地部署不受影响；部署到公网前请务必设置该环境变量并重启服务。
                <router-link to="/console/system">前往系统设置</router-link>
              </template>
            </a-alert>
            <router-view />
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.secret-banner {
  margin-bottom: var(--gap);
}
.main-layout {
  display: flex;
  flex-direction: column;
  /* 高度锁在视口里、内容区自己滚，而不是让整个文档长出去。
     原来这里是 min-height: 100vh + 文档级滚动：侧栏会被内容拉到同样的高度
     （实测日志页侧栏 5117px），滚到底时左侧菜单早就出视口了
     （实测第一个菜单项在 -4209px 处），「收起侧栏」也被顶到页面最下面。 */
  height: 100vh;
  /* 移动端浏览器（iOS Safari、Chrome Android 带地址栏时）的 100vh 是
     「地址栏收起后」的高度，于是页面底部会被地址栏盖住一截，
     而 .main-layout 是 overflow:hidden，被盖住的部分根本滚不出来。
     dvh 是**动态**视口高度，跟着地址栏伸缩走。
     两行都写：不认 dvh 的老浏览器退回上一行的 100vh，行为与现在一致。 */
  height: 100dvh;
  overflow: hidden;
  background: var(--color-bg);
}

/* ---------- 品牌（在侧栏顶部，不再单独占一条顶栏） ---------- */
.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  height: var(--size-menu-item-height);
  padding: 0 4px;
  margin-bottom: var(--gap);
  /* 自定义手型：全站默认已经是一张箭头图，浏览器不再按「这是可点的」
     自动给手型，凡是可点的地方都要自己声明（令牌见 styles/theme.css） */
  cursor: var(--cursor-hand);
  user-select: none;
}

.brand-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  border-radius: var(--radius-pill);
  /* 白字压在主色 #c87864 上只有 3.32:1，不达 AA。
     品牌标是首屏第一眼看到的东西，也是「LLM Relay」这个名字的载体，
     不该是整页最难读的一处。这里改用主色加深一档作底色：白字 4.84:1。
     深色主题会在下面再覆盖成提亮版（白字在深色底上本来就不合适）。 */
  background: var(--brand-mark-bg);
  color: var(--brand-mark-fg);
  font-size: 13px;
  font-weight: 700;
}

.brand-text {
  font-family: var(--font-family-display);
  font-size: 17px;
  font-weight: 600;
  /* 品牌名也是正文文字：主色在白底上 3.32:1、深色底上 3.98:1，都不够 */
  color: var(--text-primary-ink);
  white-space: nowrap;
  overflow: hidden;
}

/* 版本徽标的定位槽。
   徽标本体（胶囊外观、配色、弹出面板）都在 VersionBadge 组件里，
   这里只负责它在侧栏里的位置与间距 —— 组件不该知道自己在哪个布局里。

   与品牌名「LLM Relay」的左缘对齐，而不是与 LR 圆标对齐：
   .brand 的内边距 4px + 圆标 32px + 列间距 8px = 44px。
   改 .brand 的任何几何尺寸都要同步这两个 44px。 */
.brand-version-slot {
  align-self: flex-start; /* 侧栏是 flex 列，默认会拉通栏：改回贴内容宽 */
  margin-left: 44px;
  margin-top: -6px;       /* 抵掉 .brand 的 margin-bottom，避免双倍间距 */
  margin-bottom: var(--gap);
  /* 与 margin-left 配对：异常长串在组件内部截断省略，不会溢出侧栏 */
  max-width: calc(100% - 44px);
}

/* 分隔线：品牌区（身份信息）与菜单区（导航）的分界。
   1px 通栏细线用边框色，不再发明新的灰 —— 和输入框、卡片描边同源；
   上方间距来自 .brand-version-slot 的 margin-bottom，这里只管下方。 */
.sidebar-divider {
  height: 1px;
  margin-bottom: var(--gap);
  background: var(--color-border);
}

.nav-icon-btn {
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--color-border);
  border-radius: 50%;
  background: transparent;
  color: var(--color-icon);
  font-size: 15px;
  cursor: var(--cursor-hand);
  transition: background 0.2s var(--ease-expo);
}

.nav-icon-btn:hover {
  background: var(--color-icon-hover-bg);
}

/* 触摸目标：WCAG 2.2 的 2.5.8 要求可点区域至少 24×24 CSS 像素
   （按钮本身的可见尺寸 32px 已达标），这里只在**粗指针**设备上
   把纵向命中区撑到 44px —— 手机上一排小圆钮很容易点偏，
   而撑开命中区不影响桌面端的视觉密度。 */
@media (pointer: coarse) {
  .nav-icon-btn,
  .console-sidebar.is-collapsed .console-menu-item {
    min-height: 44px;
  }
}

/* 窄屏下进一步收紧内容边距：宽度本来就紧张，8px 的四周留白
   在 360px 的屏幕上等于白白吃掉 4% 的可视宽度 */
@media (max-width: 600px) {
  .content-inner { padding: 4px; }
  /* 侧栏收起态的 56px 在手机上仍偏宽，收到 44px */
  .console-sidebar.is-collapsed { width: 44px; flex-basis: 44px; }
}

/* ---------- 侧边栏 ---------- */
.main-layout-body {
  position: relative;
  flex: 1;
  min-height: 0;
}

.console-layout {
  display: flex;
  height: 100%;
  min-height: 0;
}

.console-sidebar {
  width: var(--size-sidebar-width);
  flex: 0 0 var(--size-sidebar-width);
  padding: var(--gap);
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  background: var(--color-bg);
  /* 窗口矮、菜单多时让侧栏自己滚，而不是把页面撑高 */
  overflow-y: auto;
  transition: width 0.2s var(--ease-expo), flex-basis 0.2s var(--ease-expo);
}

.sidebar-top {
  display: flex;
  flex-direction: column;
}

.console-sidebar.is-collapsed {
  width: 56px;
  flex-basis: 56px;
}

/* 收起态：这一列只剩图标，而 .console-menu-item 的左右 10px 内边距是给展开态的
   文字留的。56px 宽减去侧栏自己左右各 8px 只剩 40px，图标 15px 加上左内边距
   之后左留白 18px、右留白 23px —— 实测（.shots/measure-sidebar.mjs）整列图标比
   侧栏中轴偏左 5px，与上面居中的 LR 圆标（12/12）、底部那枚主题按钮（20.5/20.5）
   不在一条竖线上。收起态改成内容居中并去掉内边距，四处的中心线统一落到中轴上
   （实测都是 28px，即 56/2）。 */
.console-sidebar.is-collapsed .console-menu-item {
  justify-content: center;
  padding: 0;
}

.console-menu-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.console-menu-item {
  height: var(--size-menu-item-height);
  padding: 0 10px;
  display: flex;
  align-items: center;
  gap: var(--gap);
  border: none;
  border-radius: var(--radius-pill);
  background: transparent;
  color: var(--color-text);
  font-family: inherit;
  font-size: var(--font-size-menu);
  line-height: 16.56px;
  text-align: left;
  cursor: var(--cursor-hand);
  transition: background 0.2s var(--ease-expo), color 0.2s var(--ease-expo);
}

/* 实测选中态：主色 20% 透明底 + 主色文字
   文字色用 --text-primary-ink 而不是 --color-primary：
   后者在浅色底上 2.50:1、深色底上 3.66:1，菜单项是主要导航，
   读不清的代价比标题更大。底色仍用主色，观感不变。 */
.console-menu-item:hover {
  color: var(--text-primary-ink);
  background: var(--color-primary-a20);
}

.console-menu-item.active {
  color: var(--text-primary-ink);
  background: var(--color-primary-a20);
}

.console-menu-icon {
  font-size: 15px;
  flex: 0 0 15px;
}

.console-menu-label {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 底部三个动作：收起侧栏 / 退出登录 / 主题切换。
   主题按钮原来在右上角顶栏里，顶栏去掉后放到这里 —— 换主题是「跟界面有关」
   的操作，跟导航放一起比飘在内容区右上角更顺。

   竖排，不挤一行 —— 这是 2026-09-25 站主截图反馈「收…」「退…」文字被截断的修复。
   页脚可用宽 176px（192 侧栏 − 左右各 8 内边距），原来是一行三项：
   `flex: 1` 的两枚文字按钮 + 32px 灯泡 + 2×4px 间距 → 每枚文字按钮只分到 68px。
   而一枚「收起侧栏」的完整内容宽是 101px（左内边距 10 + 图标 15 + 间距 8 +
   文字 58 + 右内边距 10），文字被压到 25px，于是截断成「收…」
   （渲染态实测：label scrollWidth 58 > clientWidth 25，两枚按钮都是）。
   两枚并排无论如何都放不下（101×2 + 32 + 8 = 242 > 176），所以改成竖排：
   两枚文字按钮各占满整栏（101px 内容 + 75px 余量），文字不再参与宽度争抢。

   灯泡仍然贴右缘：与它在这一行里时的位置一致，右缘和两枚胶囊齐平。
   收起态本来就是竖排三枚居中图标（ui-spec 第 18 条钉着那三处的中心线），
   从此展开/收起是同一套排布，只是展开态文字可见。 */
.sidebar-footer {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-top: var(--gap);
}

/* 占满整栏：标签有 75px 余量，不会再被省略号截断。
   flex 显式写 0 0 auto —— 列方向下 flex: 1 是**纵向**拉伸，会把按钮撑高 */
.sidebar-footer .console-menu-item {
  flex: 0 0 auto;
  width: 100%;
}

.sidebar-footer .nav-icon-btn {
  align-self: flex-end;
}

/* 收起态（56px 宽，内容区 40px）放不下文字，只留图标 —— 三枚图标居中，
   中心线统一落到中轴 28px（=56/2），与上面的菜单图标、LR 圆标同一条线 */
.console-sidebar.is-collapsed .sidebar-footer {
  align-items: center;
  gap: var(--gap);
}

.console-sidebar.is-collapsed .sidebar-footer .console-menu-item {
  width: 32px;
}

.console-sidebar.is-collapsed .sidebar-footer .nav-icon-btn {
  align-self: center;
}

/* ---------- 内容区 ---------- */
.console-content {
  flex: 1;
  min-width: 0;
  background: var(--color-bg);
  /* 侧栏固定、只有这一块滚 —— 上面 .main-layout 锁了 100vh 之后，
     这条才真正生效（以前整个文档在滚，它是摆设） */
  overflow: auto;
}

.content-inner { padding: var(--gap); }

/* 「填满一屏」的页面（路由 meta.fill，目前只有数据看板）：内容区变成一个
   **高度确定**的纵向弹性容器。

   为什么非要确定高度：看板的日志列表要吃掉「视口减掉上方工具栏与卡片」剩下的
   高度，而「剩下多少」只有在父级高度确定时才算得出来 —— 父级若是 auto，
   flex 只能按内容分配，那块面板的高度又回到「由行数决定」，底边就跟着行数跑。

   为什么只给这一页加、其余页面保持原样：其余页面按内容自然增高（比视口高的
   如系统设置还要能整页滚），而 height: 100% 会让这类页面滚到底时少 8px 下留白 ——
   下内边距落在固定高度的盒子里，不再计入可滚范围（实测 scrollHeight 少 8px）。

   box-sizing 显式写出来：这条规则的算术（内容盒 = 视口 - 上下内边距）依赖它，
   不要靠 antd reset.css 里那条全局声明。 */
.content-inner.is-fill {
  box-sizing: border-box;
  height: 100%;
  display: flex;
  flex-direction: column;
}
</style>
