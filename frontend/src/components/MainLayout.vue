<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  DashboardOutlined,
  FileTextOutlined,
  KeyOutlined,
  ApiOutlined,
  ClusterOutlined,
  AppstoreOutlined,
  DatabaseOutlined,
  DollarOutlined,
  PartitionOutlined,
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

// 侧边栏菜单：对齐参考站 console-menu-list 的项目与顺序，
// 剔除其面向多用户的登录/工单/订单/兑换/礼品/邮件/公告模块
const menus = [
  { key: '/console/dashboard', label: '数据看板', icon: DashboardOutlined },
  { key: '/console/logs', label: '请求日志', icon: FileTextOutlined },
  { key: '/console/keys', label: '密钥信息', icon: KeyOutlined },
  { key: '/console/channels', label: '渠道管理', icon: ApiOutlined },
  { key: '/console/groups', label: '分组管理', icon: ClusterOutlined },
  { key: '/console/channel-templates', label: '模板管理', icon: AppstoreOutlined },
  { key: '/console/models-manage', label: '模型管理', icon: DatabaseOutlined },
  { key: '/console/pricing', label: '模型定价', icon: DollarOutlined },
  { key: '/console/route-analysis', label: '路由分析', icon: PartitionOutlined },
  { key: '/console/system', label: '系统看板', icon: SettingOutlined }
]

const topNav = [
  { key: '/console/dashboard', label: '控制台' },
  { key: '/console/models-manage', label: '模型' },
  { key: '/console/system', label: '系统' }
]

const collapsed = ref(false)
const isActive = (key: string) => route.path === key
const go = (key: string) => router.push(key)
</script>

<template>
  <div class="main-layout">
    <!-- 顶栏：实测高 64px，左右 padding 25px，gap 16px -->
    <header class="site-nav">
      <div class="brand" @click="go('/console/dashboard')">
        <span class="brand-mark">LR</span>
        <span class="brand-text">LLM Relay</span>
      </div>

      <nav class="nav-actions">
        <button
          v-for="n in topNav"
          :key="n.key"
          class="nav-link"
          :class="{ 'is-active': isActive(n.key) }"
          @click="go(n.key)"
        >
          {{ n.label }}
        </button>
      </nav>

      <div class="nav-spacer" />

      <div class="nav-announcement">
        <span class="nav-announcement-item">本地中转 · 端口 8888 · 数据不出内网</span>
      </div>

      <button class="nav-icon-btn" :title="themeStore.isDark ? '切换浅色' : '切换深色'" @click="themeStore.toggle()">
        <BulbOutlined v-if="!themeStore.isDark" />
        <BulbFilled v-else />
      </button>
    </header>

    <div class="main-layout-body">
      <div class="console-layout">
        <!-- 侧边栏：实测宽 224px，内边距 8px -->
        <aside class="console-sidebar" :class="{ 'is-collapsed': collapsed }">
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

          <div class="sidebar-footer">
            <button class="console-menu-item" @click="collapsed = !collapsed">
              <MenuUnfoldOutlined v-if="collapsed" class="console-menu-icon" />
              <MenuFoldOutlined v-else class="console-menu-icon" />
              <span v-if="!collapsed" class="console-menu-label">收起侧栏</span>
            </button>
          </div>
        </aside>

        <section class="console-content">
          <div class="content-inner">
            <router-view />
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.main-layout {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  background: var(--color-bg);
}

/* ---------- 顶栏 ---------- */
.site-nav {
  height: var(--size-nav-height);
  flex: 0 0 var(--size-nav-height);
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 0 25px;
  background: var(--color-bg);
}

.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  user-select: none;
}

.brand-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: var(--radius-pill);
  background: var(--color-primary);
  color: #fff;
  font-size: 13px;
  font-weight: 700;
}

.brand-text {
  font-size: 17px;
  font-weight: 600;
  color: var(--color-primary);
}

.nav-actions {
  display: flex;
  align-items: center;
  gap: var(--gap);
}

.nav-link {
  height: var(--size-menu-item-height);
  padding: 0 12px;
  display: inline-flex;
  align-items: center;
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
  background: transparent;
  color: var(--color-text);
  font-family: inherit;
  font-size: var(--font-size-menu);
  cursor: pointer;
  transition: background 0.2s var(--ease-expo), color 0.2s var(--ease-expo);
}

.nav-link:hover,
.nav-link.is-active {
  background: var(--color-primary-a20);
  color: var(--color-primary);
}

.nav-icon-btn {
  width: 32px;
  height: 32px;
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

.nav-spacer { flex: 1; }

.nav-announcement {
  display: flex;
  align-items: center;
  height: 32px;
  max-width: 46%;
  padding: 0 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  color: var(--color-text-secondary);
  font-size: 13.6px;
  overflow: hidden;
  white-space: nowrap;
}

/* ---------- 侧边栏 ---------- */
.main-layout-body {
  position: relative;
  flex: 1;
  min-height: 0;
}

.console-layout {
  display: flex;
  min-height: calc(100vh - var(--size-nav-height));
}

.console-sidebar {
  width: var(--size-sidebar-width);
  flex: 0 0 var(--size-sidebar-width);
  padding: var(--gap);
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  background: var(--color-bg);
  transition: width 0.2s var(--ease-expo), flex-basis 0.2s var(--ease-expo);
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

.sidebar-footer { padding-top: var(--gap); }

/* ---------- 内容区 ---------- */
.console-content {
  flex: 1;
  min-width: 0;
  background: var(--color-bg);
  overflow: auto;
}

.content-inner { padding: var(--gap); }
</style>
