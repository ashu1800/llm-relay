import { createApp } from 'vue'
import { createPinia } from 'pinia'
// 按需注册而不是 app.use(Antd)：全量注册会把 antd 的 139 个导出及其依赖
// 全部打进产物，入口 chunk 达到 1.45 MB（gzip 440 KB），首屏要多下几百 KB，
// 而本项目实际只用到 30 个组件。
//
// 每个组件的 install 会连它的子组件一起注册（例如 Table 会注册 Column、
// Select 会注册 Option），所以这里只列顶层组件即可。
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Col,
  ConfigProvider,
  Descriptions,
  Divider,
  Drawer,
  Form,
  Input,
  InputNumber,
  Modal,
  Popover,
  Radio,
  Row,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip
} from 'ant-design-vue'
import App from './App.vue'
import router from './router'
import { message } from './utils/feedback'
import 'ant-design-vue/dist/reset.css'
import './styles/theme.css'

const app = createApp(App)

// ---- 全局错误兜底（2026-10-11）----
//
// 错误边界之外的最后一道：App 自身的 setup、以及任何没被 ErrorBoundary
// 覆盖的组件里抛出的异常都会走到这里。此前全仓没有这个钩子，异常只会静默地
// 打断渲染 —— 用户看到界面坏了一半，线索只在控制台里。
//
// 这里只做**记录与提示**，不做界面恢复（那是 ErrorBoundary 的职责：
// 它能把出错的那一块换成带「重新加载」的面板）。两层一起用才完整。
//
// 用 utils/feedback 的 message 而不是 antd 静态方法：静态方法拿不到
// ConfigProvider 的主题上下文，8 套深色主题下会弹一个白底气泡。
app.config.errorHandler = (err, _instance, info) => {
  console.error('[vue] 未捕获的组件异常', err, info)
  message.error('界面出现异常，若操作没有生效请刷新页面重试')
}

// 只注册真正用到的组件。新增页面用到大表里的其它组件时，
// 记得回到这里补一行 —— 漏了的表现是组件不渲染（Vue 会警告未知标签）。
const components = [
  Alert,
  Button,
  Card,
  Checkbox,
  Col,
  ConfigProvider,
  Descriptions,
  Divider,
  Drawer,
  Form,
  Input,
  InputNumber,
  Modal,
  Popover,
  Radio,
  Row,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip
]
components.forEach((c) => app.use(c))

app.use(createPinia())
app.use(router)
app.mount('#app')
