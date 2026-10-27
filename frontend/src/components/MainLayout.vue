<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '@/api/client'
import { useMediaQuery } from '@/composables/useMediaQuery'
// 侧栏形态偏好（收起的图标栏 / 展开的完整侧栏）走与分页条数、列显隐同一套
// 容错读写：脏值回落默认，隐私模式下退化成「不记住」
import { readStoredChoice, writeStoredChoice } from '@/utils/persistedChoice'
import {
  DashboardOutlined,
  KeyOutlined,
  ApiOutlined,
  DollarOutlined,
  GlobalOutlined,
  SettingOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  MenuOutlined,
  LogoutOutlined,
  SearchOutlined
} from '@ant-design/icons-vue'
import { useAuthStore } from '@/stores/auth'
import { message } from '@/utils/feedback'
import { onLive } from '@/composables/useLive'
import { startTabPulse } from '@/utils/tabPulse'
import { modalWidth } from '@/utils/modalWidth'
import VersionBadge from '@/components/VersionBadge.vue'
import CommandPalette from '@/components/CommandPalette.vue'
// 页面级错误边界：某个视图渲染抛异常时兜住它，保住侧栏（见模板里的说明）
import ErrorBoundary from '@/components/ErrorBoundary.vue'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()

const paletteOpen = ref(false)
const helpModalOpen = ref(false)

// 退出登录：服务端清掉会话 Cookie，本地状态随之置否，回登录页。
// logout 内部已保证「请求失败也清本地状态」—— 会话可能早已过期。
async function logout() {
  await authStore.logout()
  router.push('/login')
}

// 默认加密密钥的提示。
//
// 用默认密钥时渠道密钥的加密等于没有：主密钥是从这个公开占位串推出来的，
// 数据库或备份文件落到别人手里就能直接解开。
// 原来只在启动日志里警告一句，journald 一滚就看不见了 ——
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
// 版本号由构建时经 -ldflags 编进二进制（见 .goreleaser.yaml 与 install.sh），
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
  // 模型定价（2026-10-27）：单价从渠道里拆出来独立成页。
  // 放在渠道管理之后：先有渠道（谁能跑），再有定价（跑一次多少钱）。
  { key: '/console/pricing', label: '模型定价', icon: DollarOutlined },
  { key: '/console/proxies', label: '代理管理', icon: GlobalOutlined },
  { key: '/console/system', label: '系统设置', icon: SettingOutlined }
]

const collapsed = ref(false)
const isActive = (key: string) => route.path === key

// 当前页名（与 document.title 同源，都取路由 meta.title）。
// 它现在有三个用途：sr-only 的 <h1>（文档结构）、窄屏的可见页名、
// 路由切换时的读屏播报。一份数据三处用，不会各自漂移。
const pageTitle = computed(() => (typeof route.meta.title === 'string' ? route.meta.title : '管理台'))

// 路由切换的读屏播报（2026-10-11）。
//
// SPA 换页时浏览器不会像多页应用那样自动播报新页面 —— 读屏用户停在原处，
// 只能靠自己按标题跳转去确认「换页了没有」。这里用一个 aria-live 区域补上。
// 只在**真的换了 path** 时写（首屏不播、query-only 变化不播）。
const routeAnnounce = ref('')

// 内容区是滚动容器（.console-content，overflow:auto），而它跨路由复用。
const contentRef = ref<HTMLElement | null>(null)

// 两条断点：900 是「侧栏收成图标栏」（平板），640 是「侧栏变抽屉」（手机）。
// 900 只作为 JS 功能断点存在（theme.css 的刻度里没有它，也不该写成 @media）。
const NARROW = '(max-width: 900px)'
const MOBILE = '(max-width: 640px)'
const isNarrow = useMediaQuery(NARROW)

// ---- 移动端抽屉（2026-10-10 窄屏改造）----
//
// 900px 那条断点把侧栏收成 56px 图标栏，在 768 的平板上正合适；
// 到 360 的手机上就不成立了：56px 是 15% 的可视宽度，而图标栏没有文字，
// 第一次用的人只能靠猜哪个图标是哪个页面。所以 ≤640 换成抽屉 ——
// 平时完全让出宽度（内容区吃满 360px），点汉堡才滑出来，带完整文字。
//
// 为什么用 JS 状态而不是纯 CSS：抽屉要带 aria-expanded / inert /
// 打开后把焦点送进去 / 关闭后把焦点还给按钮，这些都是状态，CSS 给不了。
// 另外「点了菜单项要自动收起」也只有 JS 能做。
const isMobile = useMediaQuery(MOBILE)
const navOpen = ref(false)
const navToggle = ref<HTMLButtonElement | null>(null)
const navPanel = ref<HTMLElement | null>(null)

/** 图标栏形态：只在「非移动端」跟随 collapsed。抽屉里永远是完整形态（带文字）。 */
const railMode = computed(() => collapsed.value && !isMobile.value)

function toggleNav() {
  navOpen.value = !navOpen.value
  // 打开后把焦点送进抽屉：键盘/读屏用户接下来的 Tab 应该落在菜单里，
  // 而不是继续在后面的内容区里走
  if (navOpen.value) {
    nextTick(() => navPanel.value?.querySelector<HTMLElement>('.console-menu-item')?.focus())
  }
}

/** 关闭抽屉并把焦点还给汉堡按钮（焦点留在已隐藏的子树里会丢） */
function closeNav() {
  if (!navOpen.value) return
  navOpen.value = false
  nextTick(() => navToggle.value?.focus())
}

function onKeydown(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    paletteOpen.value = !paletteOpen.value
    return
  }
  if (e.key === 'Escape' && navOpen.value) {
    closeNav()
    return
  }
  handleShortcut(e)
}

// ---- 全站快捷键（2026-10-11）----
//
// 站主的日常动线是「看板 → 找那条报错 → 去渠道页改 → 回来看」。整条动线里
// 每一步都要先用鼠标找到侧栏、再点一下 —— 一天下来是几十次多余的移动。
//
// 三组，都是编辑器里通行的做法，用户不需要学：
//   /          聚焦当前页的搜索框（VS Code / GitHub / 语雀都是它）
//   r          刷新当前页（点那颗「刷新」按钮，与手点完全同一条路径）
//   g 然后 d/k/c/f/p/s   跳到 看板 / 密钥 / 渠道 / 分组 / 代理 / 系统设置
//
// 三条约束（每一条都是「不做会出事」的）：
//   1. 焦点在输入框 / 文本域 / 下拉 / 可编辑区里时**一律不拦**。否则用户在
//      搜索框里打「r」会被抢去刷新，而这个 r 还打不进去 —— 这是快捷键最
//      经典的破坏方式。用 isContentEditable + tagName 双重判断，
//      组合框（antd 的 select 内部是真 input）因此也被覆盖。
//   2. 带修饰键的组合不处理：Ctrl+R 是浏览器刷新、Cmd+/ 是别的工具的东西，
//      抢过来会让用户觉得「这个页面把浏览器弄坏了」。
//   3. 序列键（g 之后那一下）只等 1.5 秒。等待期间用户按了别的键就当场作废，
//      不会出现「我打了 g 又打了 r，结果它还在等第二个字母」。
const SEQ_MS = 1500
let seqTimer = 0
/** 按下 g 之后的等待状态 */
const seqArmed = ref(false)

/** g 之后的字母 → 路由。字母取页面名的英文首字母，d/k/c/p/s 与侧栏顺序无关，只求好记 */
const GOTO_KEYS: Record<string, string> = {
  // eslint-disable-next-line
  d: '/console/dashboard',
  k: '/console/keys',
  c: '/console/channels',
  p: '/console/proxies',
  s: '/console/system'
}

function disarmSeq() {
  seqArmed.value = false
  if (seqTimer) {
    window.clearTimeout(seqTimer)
    seqTimer = 0
  }
}

/** 焦点是否落在一个「正在输入」的地方（见上面第 1 条约束） */
function isTypingTarget(el: EventTarget | null): boolean {
  const node = el as HTMLElement | null
  if (!node) return false
  if (node.isContentEditable) return true
  const tag = node.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

function handleShortcut(e: KeyboardEvent) {
  // 约束 2：带修饰键的一律放行（交给浏览器 / 系统）
  if (e.ctrlKey || e.metaKey || e.altKey) return
  if (isTypingTarget(e.target)) return

  if (seqArmed.value) {
    const dest = GOTO_KEYS[e.key.toLowerCase()]
    disarmSeq()
    if (dest) {
      e.preventDefault()
      go(dest)
    }
    return
  }

  if (e.key === '/') {
    // 页面上没有搜索框时什么都不做（比如看板），不要 preventDefault ——
    // 抢掉一个按键却没有任何反馈，用户会以为页面卡了
    const input = document.querySelector<HTMLElement>('.toolbar-search input, input.toolbar-search')
    if (input) {
      e.preventDefault()
      input.focus()
    }
    return
  }

  if (e.key === 'r') {
    // 与手点那一颗按钮走**完全相同**的路径（合成一次 click），而不是自己
    // 调一遍刷新逻辑：那样两边的行为迟早会分叉（比如按钮上的 loading 态、
    // 各页刷新时附带的重取）。找不到按钮就不做事（有些页面没有刷新按钮）。
    const btn = document.querySelector<HTMLButtonElement>('button[aria-label="刷新"]')
    if (btn) {
      e.preventDefault()
      btn.click()
    }
    return
  }

  if (e.key === '?') {
    e.preventDefault()
    helpModalOpen.value = !helpModalOpen.value
    return
  }

  if (e.key === 'g') {
    seqArmed.value = true
    seqTimer = window.setTimeout(disarmSeq, SEQ_MS)
  }
}

// 从抽屉里点菜单项导航：抽屉一关，焦点原本所在的菜单项就落进了隐藏子树
// （inert + visibility:hidden），焦点会掉回 body —— 读屏用户因此听不到
// 「页面换了」。交给内容区：<main tabindex="-1"> 正是为程序化聚焦准备的。
let focusMainAfterNav = false
const go = (key: string) => {
  if (isMobile.value) {
    if (navOpen.value) focusMainAfterNav = true
    navOpen.value = false
  }
  router.push(key)
}
// 任何导航（含浏览器后退、跳过导航链接）都收起抽屉，并把内容区滚回顶部。
watch(
  () => route.path,
  () => {
    navOpen.value = false
    // ---- 滚动位置重置（2026-10-11）----
    //
    // 滚动容器是 .console-content，而它跨路由复用（只有 <router-view> 里的内容换）。
    // 不重置的话，从滚到底的「系统设置」切到「渠道管理」会落在新页中段 ——
    // 浏览器只会把 scrollTop 夹到新页的最大值，不会自动回到顶部；
    // 而 router 的 scrollBehavior 管的是文档滚动，对这个容器完全不生效
    // （全仓也从来没有人重置过它）。
    //
    // 只在 **path** 变化时置 0：query-only 的变化（?trace_id= 这类排障深链）
    // 是「同一个页面换个视角」，把用户正在看的位置弹回顶部反而更烦。
    if (contentRef.value) contentRef.value.scrollTop = 0
    routeAnnounce.value = '已进入 ' + pageTitle.value
    if (focusMainAfterNav) {
      focusMainAfterNav = false
      nextTick(() => document.getElementById('main-content')?.focus())
    }
  }
)

// ---- 窄屏自动收起侧栏 ----
//
// 侧栏固定 192px。窗口一窄，它就要占掉一大半宽度，内容区被压成一条缝 ——
// 表格虽然有横向滚动（各页都配了 :scroll="{ x }"），但连「一屏能看见两列」
// 都做不到时，滚动也救不回来。
//
// 跨过断点时跟随视口，但用户在同一档内手动展开/收起后不再被覆盖 ——
// 所以只在断点**变化**时写 collapsed，不在每次 resize 时写
// （immediate 的那一次就是首屏取值：窄屏进来时不该先闪一下展开态）。
//
// 站主的形态偏好也记在这里（2026-10-11）：常用图标栏的人每次刷新都要再收一次，
// 是纯粹的重复劳动。两条规则的分工是：
//   窄屏 → 一律收起（这条是硬规则，屏幕就那么大）；
//   宽屏 → 用记下来的偏好（没记过就是展开）。
// 断点自动引起的变化**不写**偏好 —— 不然「在窄屏下自动收起」会把用户在宽屏下
// 选过的展开态覆盖掉，回到宽屏就永远是收起的了。
const COLLAPSE_KEY = 'sidebar-collapsed'
// 这次 collapsed 变化是不是断点自动造成的（见下面的 watch）
let autoCollapse = false
watch(
  isNarrow,
  (narrow) => {
    const want = narrow || readStoredChoice(COLLAPSE_KEY, ['0', '1'], '0') === '1'
    // 提前返回而不是照样赋值：赋值不改变值时下面的 watch 不会跑，
    // 那个「这次是自动的」标记就会留到下一次**手动**点击时才被消费掉，
    // 表现为「点了收起，刷新后没记住」
    if (want === collapsed.value) return
    autoCollapse = true
    collapsed.value = want
  },
  { immediate: true }
)
watch(collapsed, (v) => {
  if (autoCollapse) {
    autoCollapse = false
    return
  }
  writeStoredChoice(COLLAPSE_KEY, v ? '1' : '0')
})
// 从窄屏回到宽屏时把抽屉状态收干净，否则回到桌面会带着一个「开着」的抽屉
watch(isMobile, (mobile) => {
  if (!mobile) navOpen.value = false
})

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  // 页签心跳：角标显示今日请求数，熔断/超支转红（数据走已有 live 推送）。
  // MainLayout 常驻，这里启动一次就覆盖整个会话
  startTabPulse()
  // 系统信息与窄屏初始化合在同一个 onMounted：原来有两个，各自请求一次
  // /system/info —— 每次进页面白打一个重复请求（窄屏适配改造时留下的）。
  //
  // 版本号曾经也搭这个请求顺路带回来。现在它由 VersionBadge 自己取
  // （见 stores/version.ts 的 fetchInfo）：那个组件需要一个结构化的
  // 版本对象（构建形态、是否 release、能否一键更新），而 /system/info
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
  window.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="main-layout">
    <!-- 跳过导航（2026-10-10 无障碍）：键盘用户每进一个页面都要 Tab 过
         6 个菜单项 + 2 个页脚按钮才能到内容区，而这是每次导航都要付的成本。
         它平时被移出视口（不能用 display:none —— 那样不可聚焦），
         按 Tab 时滑入左上角；回车跳到下面的 <main>。 -->
    <a class="skip-link" href="#main-content">跳到主内容</a>
    <!-- 没有顶栏：品牌归到侧栏。
         参考站有一条 64px 的顶栏，但它装的是公告条和「控制台/模型/文档/头像」，
         我们这边只剩品牌一个元素 —— 留着就是一行空白，还把每个页面的内容
         整体压下 64px。品牌移到侧栏顶部之后，右侧内容直接顶到最上面。
         主题切换 2026-09-26 起改到看板页工具栏右上角（刷新按钮左边），
         侧栏不再承担它。 -->
    <div class="main-layout-body">
      <div class="console-layout">
        <!-- 侧边栏：实测宽 192px，内边距 8px -->
        <!-- 遮罩：抽屉打开时盖住内容区，点它关闭。aria-hidden —— 它只是个
             点击目标，对读屏没有意义（关闭动作由 Esc 与汉堡按钮的 aria-expanded
             表达） -->
        <div v-if="isMobile && navOpen" class="nav-scrim" aria-hidden="true" @click="closeNav()"></div>
        <!-- 侧边栏：实测宽 192px，内边距 8px。
             ≤640 时它是抽屉（is-mobile / is-open）；关闭状态下 inert ——
             移出无障碍树与 Tab 序，否则 Tab 会走进一个看不见的菜单。 -->
        <aside
          id="console-nav"
          ref="navPanel"
          class="console-sidebar"
          :class="{ 'is-collapsed': railMode, 'is-mobile': isMobile, 'is-open': navOpen }"
          :inert="isMobile && !navOpen ? true : undefined"
        >
          <div class="sidebar-top">
            <!-- 品牌区：点击回数据看板。
                 用 <button> 而不是 <div @click>（2026-10-11）：没有 tabindex 的 div
                 键盘永远聚焦不到、回车也触发不了 —— 它是导航，不是装饰。
                 展开态的可访问名就是可见文字「LLM Relay」（不写 aria-label：
                 写了会让名字与可见文字不一致，违反 WCAG 2.5.3「名称中含可见标签」）；
                 收起态没有可见文字，才补 aria-label。 -->
            <button
              type="button"
              class="brand"
              :title="railMode ? '回到数据看板' : ''"
              :aria-label="railMode ? '回到数据看板' : undefined"
              @click="go('/console/dashboard')"
            >
              <span class="brand-mark" aria-hidden="true">LR</span>
              <span v-if="!railMode" class="brand-text">LLM Relay</span>
            </button>

            <!-- 版本号紧随品牌：它是这个应用的身份信息，与品牌同属一组。
                收起侧栏时不显示（那一列只有 40px 宽，塞不下）；
                窄屏下侧栏默认收起，所以它在窄屏是不可见的 —— 这是有意的，
                窄屏空间该留给内容，查版本可以用 deploy/install.sh --verify。 -->
            <VersionBadge v-if="!railMode" class="brand-version-slot" />

            <!-- 品牌区（身份信息）与菜单区（导航）的分界。收起态不显示：
                 那时上下都是纯图标，一条线反而显得挤 -->
            <div v-if="!railMode" class="sidebar-divider" aria-hidden="true"></div>

            <nav class="console-menu-list">
              <button
                v-for="m in menus"
                :key="m.key"
                class="console-menu-item"
                :class="{ active: isActive(m.key) }"
                :title="m.label"
                :aria-current="isActive(m.key) ? 'page' : undefined"
                @click="go(m.key)"
              >
                <component :is="m.icon" class="console-menu-icon" />
                <span v-if="!railMode" class="console-menu-label">{{ m.label }}</span>
              </button>
            </nav>
          </div>

          <div class="sidebar-footer">
            <!-- 收起时只剩一个图标，读屏会念成「menu-fold」这种图标名。
                 aria-label 给它一个真实的动作名；展开态有可见文字，
                 aria-label 与之一致即可，不会重复朗读。 -->
            <button
              v-if="!isMobile"
              class="console-menu-item"
              :aria-label="railMode ? '展开侧栏' : '收起侧栏'"
              :aria-expanded="!railMode"
              @click="collapsed = !collapsed"
            >
              <MenuUnfoldOutlined v-if="railMode" class="console-menu-icon" aria-hidden="true" />
              <MenuFoldOutlined v-else class="console-menu-icon" aria-hidden="true" />
              <span v-if="!railMode" class="console-menu-label">收起侧栏</span>
            </button>
            <!-- 退出登录（无账号模型：清掉会话 Cookie 回登录页）。
                 收起态同样只留图标，aria-label 保证动作可读 -->
            <button class="console-menu-item" aria-label="退出登录" @click="logout">
              <LogoutOutlined class="console-menu-icon" aria-hidden="true" />
              <span v-if="!railMode" class="console-menu-label">退出登录</span>
            </button>
            <!-- 命令面板入口（2026-10-26 应站主要求从菜单顶部移到侧栏最底部）：
                  它是全局工具而非页面导航，与「收起侧栏 / 退出登录」同属动作区，
                  不该插在页面菜单中间打断「看板 → … → 设置」的顺序 -->
            <button
              type="button"
              class="console-menu-item command-palette-trigger"
              :aria-label="'打开命令面板 (Ctrl+K)'"
              :title="'命令面板 (Ctrl+K)'"
              @click="paletteOpen = true"
            >
              <SearchOutlined class="console-menu-icon" aria-hidden="true" />
              <span v-if="!railMode" class="console-menu-label">命令面板</span>
              <kbd v-if="!railMode" class="palette-mini-kbd">Ctrl K</kbd>
            </button>
            <!-- 图标栏形态下的版本入口（2026-10-11）：品牌名下方那枚徽标在
                 收起时不渲染，于是「有没有新版本」在 56px 形态下没有任何入口，
                 而更新提示正是用户需要知道的信息。这里补一个 icon-only 的
                 同一组件（面板与逻辑完全共用），展开态不渲染它 ——
                 同一信息不该在侧栏里出现两次。 -->
            <VersionBadge v-if="railMode" icon-only class="rail-version-slot" />
          </div>
        </aside>

        <!-- 内容区是 <main> 地标（2026-10-10 无障碍）：读屏用户靠地标在
             页面结构里跳转，原来整页只有 <aside> 和一堆 <section>，
             「主要内容在哪」没有任何标记。id 给上面的跳过导航用，
             tabindex=-1 让它能被程序化聚焦（fragment 导航本身不会移动焦点）。 -->
        <main
          id="main-content"
          ref="contentRef"
          class="console-content"
          tabindex="-1"
          :inert="isMobile && navOpen ? true : undefined"
        >
          <!-- 移动端顶部条（≤640）：汉堡 + 当前页名。桌面上不渲染 ——
               没有顶栏是桌面端有意的取舍（品牌归侧栏，见上面那段注释），
               而手机上侧栏是抽屉，需要一个常驻的入口。
               这里显示**当前页名**而不是品牌名（2026-10-11）：≤640 时侧栏默认
               关着、菜单文字也全部隐藏，用户除了浏览器标签标题之外没有任何
               线索能确认自己在哪一页；而品牌名在标签栏与登录页都有，重复它
               等于浪费手机上最宝贵的一行空间。 -->
          <div v-if="isMobile" class="mobile-bar">
            <button
              ref="navToggle"
              type="button"
              class="mobile-nav-btn"
              aria-controls="console-nav"
              :aria-expanded="navOpen"
              :aria-label="navOpen ? '关闭导航菜单' : '打开导航菜单'"
              @click="toggleNav"
            >
              <MenuOutlined aria-hidden="true" />
            </button>
            <span class="mobile-page-title">{{ pageTitle }}</span>
          </div>
          <!-- is-fill 由路由的 meta.fill 给（目前只有数据看板）：这一页要填满一屏，
               内容区必须有确定高度，页面里那条弹性链才有「剩余多少」可分 -->
          <div class="content-inner" :class="{ 'is-fill': route.meta.fill }">
            <!-- 页面标题（2026-10-10 标题层级收口）。
                 视觉上页面名已经由侧栏选中项与各页自己的标题表达，这里**不**再画一遍；
                 但文档结构需要它：此前全站只有登录页有一个 <h1>，各管理页从 h2
                 （面板标题）起步，读屏用户按标题跳转时没有「当前在哪一页」的锚点。
                 放在 MainLayout 而不是每个视图各写一份：一处维护、以后新增页面自动有，
                 文案直接取路由 meta.title（与 document.title 同源）。
                 .sr-only 见 theme.css —— 不能用 display:none，那会让它从无障碍树上消失。 -->
            <h1 class="sr-only">{{ pageTitle }}</h1>
            <!-- 图标栏形态下（641–900px）侧栏只剩图标、菜单文字全部隐藏，
                 而上面的 h1 是 sr-only —— 屏幕上没有任何「当前在哪一页」的文字，
                 用户只能靠浏览器标签标题确认位置。这里补一行小号页名，
                 取同一份 meta.title（不新增维护点）。 -->
            <div v-if="railMode" class="rail-page-title">{{ pageTitle }}</div>
            <!-- 路由切换的读屏播报：SPA 换页浏览器不会自动播报（见 script 说明） -->
            <p class="sr-only" aria-live="polite">{{ routeAnnounce }}</p>
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
            <!-- 页面级错误边界（2026-10-11）：坏掉的只是某一个视图时，
                 侧栏与移动端顶栏仍然可用 —— 用户可以点别的页面走开，
                 而不是面对一张空白页只能刷新。
                 App.vue 里还有一层（兜外壳本身的错误）：两层嵌套时内层先接住
                 （onErrorCaptured 返回 false 就不再向上冒泡）。 -->
            <ErrorBoundary>
              <router-view v-slot="{ Component }">
                <Transition name="route-fade" mode="out-in">
                  <component :is="Component" />
                </Transition>
              </router-view>
            </ErrorBoundary>
          </div>
        </main>
      </div>
    </div>
    <CommandPalette v-model:open="paletteOpen" />

    <!-- 全站极客快捷键图谱 HUD（方案 10） -->
    <a-modal
      v-model:open="helpModalOpen"
      title="极客快捷键全景图谱"
      :width="modalWidth(560)"
      :footer="null"
      centered
      class="keymap-modal"
    >
      <div class="keymap-hud">
        <div class="keymap-section">
          <div class="keymap-sec-title">全局导航与直达</div>
          <div class="keymap-grid">
            <div class="keymap-row">
              <span class="keymap-desc">唤出命令面板</span>
              <span class="keymap-keys"><kbd class="geek-kbd">Ctrl</kbd> + <kbd class="geek-kbd">K</kbd></span>
            </div>
            <div class="keymap-row">
              <span class="keymap-desc">直达各管理页面</span>
              <span class="keymap-keys"><kbd class="geek-kbd">G</kbd> 然后 <kbd class="geek-kbd">D / K / C / P / S</kbd></span>
            </div>
            <div class="keymap-row">
              <span class="keymap-desc">聚焦搜索框</span>
              <span class="keymap-keys"><kbd class="geek-kbd">/</kbd></span>
            </div>
            <div class="keymap-row">
              <span class="keymap-desc">刷新当前数据</span>
              <span class="keymap-keys"><kbd class="geek-kbd">R</kbd></span>
            </div>
          </div>
        </div>

        <div class="keymap-section">
          <div class="keymap-sec-title">日志表格极客排障</div>
          <div class="keymap-grid">
            <div class="keymap-row">
              <span class="keymap-desc">上下行穿梭</span>
              <span class="keymap-keys"><kbd class="geek-kbd">J</kbd> / <kbd class="geek-kbd">K</kbd> 或 <kbd class="geek-kbd">↓</kbd> / <kbd class="geek-kbd">↑</kbd></span>
            </div>
            <div class="keymap-row">
              <span class="keymap-desc">展开/关闭调用详情</span>
              <span class="keymap-keys"><kbd class="geek-kbd">Space</kbd> / <kbd class="geek-kbd">Enter</kbd></span>
            </div>
            <div class="keymap-row">
              <span class="keymap-desc">快速复制 Trace ID</span>
              <span class="keymap-keys"><kbd class="geek-kbd">C</kbd></span>
            </div>
            <div class="keymap-row">
              <span class="keymap-desc">查看此帮助图谱</span>
              <span class="keymap-keys"><kbd class="geek-kbd">?</kbd></span>
            </div>
          </div>
        </div>
      </div>
    </a-modal>
  </div>
</template>

<style scoped>
.secret-banner {
  margin-bottom: var(--gap);
}

/* 跳过导航（2026-10-10 无障碍）。
   平时用 translateY(-200%) 移出视口而不是 display:none / visibility:hidden ——
   后两者会让它从无障碍树上消失，键盘也 Tab 不到，等于没有这个功能。
   聚焦时滑回左上角；配色走主题令牌，深色主题下也是深底浅字。 */
.skip-link {
  position: fixed;
  top: 8px;
  left: 8px;
  z-index: 100;
  padding: 8px 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-fg);
  color: var(--text-primary-ink);
  font-size: var(--font-size-md);
  text-decoration: none;
  transform: translateY(-200%);
  transition: transform 0.15s var(--ease-expo);
}

/* 用 :focus 而不是 :focus-visible：跳过导航只可能由键盘到达（Tab 的第一站），
   而 :focus-visible 对**程序化** focus() 是否命中由浏览器自己判断 ——
   实测在 headless Chrome 里不命中，于是「聚焦了但链接还在视口外」。
   这里用 :focus 表达的是「只要它拿到焦点就可见」，语义更直接。 */
.skip-link:focus {
  transform: translateY(0);
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
/* 它是 <button>（键盘可达，见模板注释）：浏览器默认样式要逐项清掉 ——
   否则会出现灰色底、居中文字与 2px 内缩边框，与原来那个 div 长得不一样。
   width: 100% 让它撑满侧栏（原来 div 是块级、天然撑满）。 */
.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: var(--size-menu-item-height);
  padding: 0 4px;
  margin-bottom: var(--gap);
  border: none;
  background: none;
  font-family: inherit;
  text-align: left;
  /* 自定义手型：全站默认已经是一张箭头图，浏览器不再按「这是可点的」
     自动给手型，凡是可点的地方都要自己声明（令牌见 styles/theme.css） */
  cursor: var(--cursor-hand);
  user-select: none;
}

/* 悬停反馈与菜单项同一套（主色 20% 底），但只在展开态给：
   收起态那一列只有 40px 宽，一个 32px 圆标外面套一层底色会显得挤 */
.brand:hover .brand-mark {
  filter: brightness(1.08);
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
  font-size: var(--font-size-md);
  font-weight: 700;
  box-shadow: 0 0 10px color-mix(in oklab, var(--brand-mark-bg) 50%, transparent),
              inset 0 1px 0 rgba(255, 255, 255, 0.3);
  transition: transform 0.25s var(--ease-expo), box-shadow 0.25s ease;
}

.brand:hover .brand-mark {
  transform: scale(1.05);
  box-shadow: 0 0 16px color-mix(in oklab, var(--brand-mark-bg) 80%, transparent),
              inset 0 1px 0 rgba(255, 255, 255, 0.45);
}

.brand-text {
  font-family: var(--font-family-display);
  font-size: var(--font-size-xl);
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

/* 触摸目标：WCAG 2.2 的 2.5.8 要求可点区域至少 24×24 CSS 像素。
   这里只在**粗指针**设备上把收起态菜单项（纯图标、无文字）的
   纵向命中区撑到 44px —— 手机上纯图标按钮很容易点偏，
   而撑开命中区不影响桌面端的视觉密度。 */
@media (pointer: coarse) {
  .console-sidebar.is-collapsed .console-menu-item {
    min-height: 44px;
  }
}

/* 窄屏下进一步收紧内容边距：宽度本来就紧张，8px 的四周留白
   在 360px 的屏幕上等于白白吃掉 4% 的可视宽度。
   640 是全站断点基准的最小一档（见 theme.css「响应式断点」一节），
   原来是 600 —— 对齐后 600-640 区间的手机也提前收到紧凑边距。 */
@media (max-width: 640px) {
  .content-inner { padding: 4px; }

  /* ---- 侧栏改成抽屉（2026-10-10）----
     900px 那条断点把侧栏收成 56px 图标栏，768 的平板上正合适；
     到 360 的手机上就不成立了：56px 是 15% 的可视宽度，而图标栏没有文字。
     抽屉平时完全让出宽度，点汉堡才滑出来，带完整文字。 */
  .console-layout { display: block; }
  .console-sidebar {
    position: fixed;
    inset: 0 auto 0 0;
    width: 224px;
    flex-basis: 224px;
    z-index: 1200;
    box-shadow: var(--card-hover-shadow);
    transform: translateX(-100%);
    visibility: hidden;
    /* 关闭态把 visibility 延迟到滑动结束：否则抽屉是「瞬间消失」的，
       看不到滑出动画（visibility 本身不可动画，只能靠 delay 接力） */
    transition: transform 0.18s var(--ease-expo), visibility 0s linear 0.18s;
  }
  .console-sidebar.is-open {
    transform: none;
    visibility: visible;
    transition: transform 0.18s var(--ease-expo), visibility 0s;
  }
  /* 抽屉里是完整形态：菜单项撑到 44px 的触摸目标（WCAG 2.5.8 的 24px 是下限，
     44 才是好点的尺寸），桌面端的紧凑密度不受影响 */
  .console-sidebar.is-mobile .console-menu-item { min-height: 44px; }

  .nav-scrim {
    position: fixed;
    inset: 0;
    z-index: 1190;
    background: rgba(0, 0, 0, 0.45);
  }

  /* 内容区：移动端顶部多了一条 44px 的移动栏，所以改成纵向弹性容器 ——
     fill 页面的高度链从「height: 100%」换成「flex: 1」，
     否则 44px + 100% 会溢出视口（看板正是 fill 页面） */
  .console-content {
    height: 100%;
    display: flex;
    flex-direction: column;
  }
  .mobile-bar {
    display: flex;
    flex: none;
    align-items: center;
    gap: 8px;
    height: 44px;
    padding: 0 4px;
  }
  .mobile-nav-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 44px;
    height: 44px;
    padding: 0;
    border: none;
    background: none;
    color: var(--color-text);
    font-size: var(--font-size-display-sm);
    border-radius: var(--radius-panel);
    cursor: var(--cursor-hand);
  }
  .mobile-nav-btn:hover { background: var(--color-icon-hover-bg); }
  /* 移动端顶栏里的当前页名。正文色 + 常规字重（不是品牌名那套衬线 + 700）——
     它表达的是「你在哪一页」，与品牌的身份感不是一回事。
     超长页名截断：路由标题是我们自己写的，但没有理由让它把汉堡挤出屏幕。 */
  .mobile-page-title {
    font-size: var(--font-size-xl);
    font-weight: 600;
    color: var(--text-primary-ink);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    /* flex 项默认 min-width:auto，不加这行 ellipsis 不会生效（会被文字顶宽） */
    min-width: 0;
  }
  .content-inner.is-fill {
    height: auto;
    flex: 1;
    min-height: 0;
  }
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
   侧栏中轴偏左 5px，与上面居中的 LR 圆标（12/12）不在一条竖线上。
   收起态改成内容居中并去掉内边距，各处中心线统一落到中轴上
   （实测都是 28px，即 56/2）。 */
.console-sidebar.is-collapsed .console-menu-item {
  justify-content: center;
  padding: 0;
  transition: transform 0.2s cubic-bezier(0.34, 1.56, 0.64, 1), background 0.2s ease, color 0.2s ease;
}

.console-sidebar.is-collapsed .console-menu-item:hover {
  transform: scale(1.12);
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

.console-menu-item:not(:disabled):active {
  transform: scale(0.96);
  transition: transform 0.08s ease;
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
  font-size: var(--font-size-lg);
  flex: 0 0 15px;
}

.console-menu-label {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 底部两个动作：收起侧栏 / 退出登录。
   （主题切换 2026-09-26 起迁去看板页工具栏右上角，这里不再承担它。）

   竖排，不挤一行 —— 这是 2026-09-25 站主截图反馈「收…」「退…」文字被截断的修复。
   页脚可用宽 176px（192 侧栏 − 左右各 8 内边距），原来是一行三项：
   `flex: 1` 的两枚文字按钮 + 32px 灯泡 + 2×4px 间距 → 每枚文字按钮只分到 68px。
   而一枚「收起侧栏」的完整内容宽是 101px（左内边距 10 + 图标 15 + 间距 8 +
   文字 58 + 右内边距 10），文字被压到 25px，于是截断成「收…」
   （渲染态实测：label scrollWidth 58 > clientWidth 25，两枚按钮都是）。
   两枚并排仍放不下（101×2 + 4 = 206 > 176），所以改成竖排：
   两枚文字按钮各占满整栏（101px 内容 + 75px 余量），文字不再参与宽度争抢。
   展开态两枚胶囊贴右缘，与菜单项同宽同形；

   收起态本来就是竖排图标居中（ui-spec 第 18 条钉着那几处的中心线），
   展开/收起是同一套排布，只是展开态文字可见。 */
.sidebar-footer {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-top: var(--gap);
}

.palette-mini-kbd {
  font-size: var(--font-size-2xs);
  padding: 1px 5px;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-text-secondary) 12%, transparent);
  color: var(--color-text-secondary);
  border: 1px solid var(--color-border);
  margin-left: auto;
  line-height: 1.2;
}

/* 占满整栏：标签有 75px 余量，不会再被省略号截断。
   flex 显式写 0 0 auto —— 列方向下 flex: 1 是**纵向**拉伸，会把按钮撑高 */
.sidebar-footer .console-menu-item {
  flex: 0 0 auto;
  width: 100%;
}

/* 收起态（56px 宽，内容区 40px）放不下文字，只留图标 —— 图标居中，
   中心线统一落到中轴 28px（=56/2），与上面的菜单图标、LR 圆标同一条线 */
.console-sidebar.is-collapsed .sidebar-footer {
  align-items: center;
  gap: var(--gap);
}

/* 图标栏里的版本入口：与「展开侧栏」「退出登录」两枚图标同一竖线。
   自身不占整栏宽（那会让 28px 的方形按钮被拉成 40px 的横条）。 */
.rail-version-slot {
  align-self: center;
}

.console-sidebar.is-collapsed .sidebar-footer .console-menu-item {
  width: 32px;
}

/* ---------- 内容区 ---------- */
.console-content {
  flex: 1;
  min-width: 0;
  background: var(--color-bg);
  background-image: radial-gradient(
    color-mix(in oklab, var(--color-text) 3.5%, transparent) 1px,
    transparent 1px
  );
  background-size: 24px 24px;
  /* 侧栏固定、只有这一块滚 —— 上面 .main-layout 锁了 100vh 之后，
     这条才真正生效（以前整个文档在滚，它是摆设） */
  overflow: auto;
}

.content-inner { padding: var(--gap); }

/* 图标栏形态下的可见页名（641–900px）。
   侧栏只剩图标、菜单文字全隐藏，而 h1 是 sr-only —— 屏幕上没有别的
   「当前在哪一页」的文字。字号与颜色跟面板标题同一档（次要色、12px），
   它不是标题而是定位信息，不该与页面内容抢层级。 */
.rail-page-title {
  margin: 0 0 var(--gap);
  font-size: var(--font-size-md);
  color: var(--color-text-secondary);
}

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

/* ---- 极客快捷键全景图谱（方案 10） ---- */
.keymap-hud {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 6px 0 2px;
}

.keymap-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.keymap-sec-title {
  font-size: var(--font-size-xs);
  font-weight: 600;
  color: var(--text-primary-ink);
  padding-bottom: 4px;
  border-bottom: 1px dashed var(--color-border);
}

.keymap-grid {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.keymap-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: var(--font-size-sm);
}

.keymap-desc {
  color: var(--color-text-secondary);
}

.keymap-keys {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: var(--font-size-xs);
}

.geek-kbd {
  display: inline-block;
  padding: 2px 7px;
  font-family: var(--font-family-mono);
  font-size: var(--font-size-2xs);
  line-height: 1.2;
  color: var(--color-text);
  background: color-mix(in oklab, var(--color-bg) 65%, var(--color-fg));
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  box-shadow: 0 2px 0 var(--color-border), 0 3px 6px rgba(0, 0, 0, 0.1);
  transform: translateY(-1px);
}

/* ---- 路由视图流体交叉溶解（方案 10） ---- */
.route-fade-enter-active,
.route-fade-leave-active {
  transition: opacity 0.14s ease, transform 0.14s ease;
}

.route-fade-enter-from {
  opacity: 0;
  transform: translateY(3px);
}

.route-fade-leave-to {
  opacity: 0;
  transform: translateY(-2px);
}
</style>
