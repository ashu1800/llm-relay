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
  Col,
  ConfigProvider,
  Descriptions,
  Divider,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
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
import 'ant-design-vue/dist/reset.css'
import './styles/theme.css'

const app = createApp(App)

// 只注册真正用到的组件。新增页面用到大表里的其它组件时，
// 记得回到这里补一行 —— 漏了的表现是组件不渲染（Vue 会警告未知标签）。
const components = [
  Alert,
  Button,
  Card,
  Col,
  ConfigProvider,
  Descriptions,
  Divider,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
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
