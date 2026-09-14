<script setup lang="ts">
import { onMounted, ref } from 'vue'
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
            <button class="console-menu-item" @click="collapsed = !collapsed">
              <MenuUnfoldOutlined v-if="collapsed" class="console-menu-icon" />
              <MenuFoldOutlined v-else class="console-menu-icon" />
              <span v-if="!collapsed" class="console-menu-label">收起侧栏</span>
            </button>
            <button
              class="nav-icon-btn"
              :title="themeStore.isDark ? '切换浅色' : '切换深色'"
              @click="themeStore.toggle()"
            >
              <BulbOutlined v-if="!themeStore.isDark" />
              <BulbFilled v-else />
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
  cursor: pointer;
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
  background: var(--color-primary);
  color: #fff;
  font-size: 13px;
  font-weight: 700;
}

.brand-text {
  font-family: var(--font-family-display);
  font-size: 17px;
  font-weight: 600;
  color: var(--color-primary);
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
  cursor: pointer;
  transition: background 0.2s var(--ease-expo);
}

.nav-icon-btn:hover {
  background: var(--color-icon-hover-bg);
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
  cursor: pointer;
  transition: background 0.2s var(--ease-expo), color 0.2s var(--ease-expo);
}

.console-menu-item:hover {
  color: var(--color-primary);
  background: var(--color-primary-a20);
}

/* 实测选中态：主色 20% 透明底 + 主色文字 */
.console-menu-item.active {
  color: var(--color-primary);
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
