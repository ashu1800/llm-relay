<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '@/api/client'
import {
  DashboardOutlined,
  FileTextOutlined,
  KeyOutlined,
  ApiOutlined,
  ClusterOutlined,
  GlobalOutlined,
  SettingOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  BulbOutlined,
  BulbFilled
} from '@ant-design/icons-vue'
import { useThemeStore } from '@/stores/theme'

const route = useRoute()
const router = useRouter()
const themeStore = useThemeStore()

// 默认加密密钥的提示。
//
// 用默认密钥时渠道密钥的加密等于没有：主密钥是从这个公开占位串推出来的，
// 数据库或备份文件落到别人手里就能直接解开。
// 原来只在启动日志里警告一句，容器日志一滚就看不见了 ——
// 这种事必须持续可见，所以放在界面上。
const usingDefaultSecret = ref(false)

onMounted(async () => {
  try {
    const info = await api.get<{ using_default_secret?: boolean }>('/system/info')
    usingDefaultSecret.value = !!info.using_default_secret
  } catch {
    // 拿不到系统信息不影响正常使用，静默即可
  }
})

// 侧边栏菜单：对齐参考站 console-menu-list 的项目与顺序，
// 剔除其面向多用户的登录/工单/订单/兑换/礼品/邮件/公告模块
const menus = [
  { key: '/console/dashboard', label: '数据看板', icon: DashboardOutlined },
  { key: '/console/logs', label: '请求日志', icon: FileTextOutlined },
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
// 侧栏固定 224px。窗口一窄，它就要占掉一大半宽度，内容区被压成一条缝 ——
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

onMounted(async () => {
  if (typeof window !== 'undefined' && window.matchMedia) {
    narrowMq = window.matchMedia(NARROW)
    // 首屏就按当前宽度定：窄屏进来时不该先闪一下展开态
    collapsed.value = narrowMq.matches
    narrowMq.addEventListener('change', onNarrowChange)
  }
  try {
    const info = await api.get<{ using_default_secret?: boolean }>('/system/info')
    usingDefaultSecret.value = !!info.using_default_secret
  } catch {
    // 拿不到系统信息不影响正常使用，静默即可
  }
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
        <!-- 侧边栏：实测宽 224px，内边距 8px -->
        <aside class="console-sidebar" :class="{ 'is-collapsed': collapsed }">
          <div class="sidebar-top">
            <div class="brand" :title="collapsed ? 'LLM Relay' : ''" @click="go('/console/dashboard')">
              <span class="brand-mark">LR</span>
              <span v-if="!collapsed" class="brand-text">LLM Relay</span>
            </div>

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
            <button
              class="nav-icon-btn"
              :title="themeStore.isDark ? '切换浅色' : '切换深色'"
              :aria-label="themeStore.isDark ? '切换到浅色主题' : '切换到深色主题'"
              :aria-pressed="themeStore.isDark"
              @click="themeStore.toggle()"
            >
              <BulbOutlined v-if="!themeStore.isDark" aria-hidden="true" />
              <BulbFilled v-else aria-hidden="true" />
            </button>
          </div>
        </aside>

        <section class="console-content">
          <div class="content-inner">
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

/* 底部一行：收起侧栏（占满剩余宽度）+ 主题切换。
   主题按钮原来在右上角顶栏里，顶栏去掉后放到这里 —— 换主题是「跟界面有关」
   的操作，跟导航放一起比飘在内容区右上角更顺。
   收起态（56px 宽）放不下一行两项，改成竖排两枚图标。 */
.sidebar-footer {
  display: flex;
  align-items: center;
  gap: 4px;
  padding-top: var(--gap);
}

.sidebar-footer .console-menu-item { flex: 1; min-width: 0; }

.console-sidebar.is-collapsed .sidebar-footer {
  flex-direction: column;
  gap: var(--gap);
}

.console-sidebar.is-collapsed .sidebar-footer .console-menu-item {
  flex: 0 0 auto;
  width: 32px;
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
</style>
