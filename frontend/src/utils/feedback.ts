// 全站轻提示（message）与确认弹窗（Modal.confirm）的唯一出口。
//
// ---- 为什么需要这一层（2026-10-10 UI 打磨）----
//
// antd 的**静态方法**拿不到 <a-config-provider> 的上下文。官方文档原话：
//   "ConfigProvider will not take effect on static methods such as
//    message.xxx, Modal.xxx, notification.xxx, because in these methods,
//    ant-design-vue will dynamically create new ones through render Vue
//    entities. Its context is not the same as the context of the current code"
//   （ant-design-vue：site/src/vueDocs/customize-theme.en-US.md）
//
// 机制上也对得上：静态路径各自 createVNode 到 body 下的一个游离容器，
// 读的只是 config-provider 的 globalConfigForApi（模块级全局，没人写过），
// 见 node_modules/ant-design-vue/es/modal/confirm.js 的 Wrapper 与
// es/message/index.js → es/vc-notification/Notification.js:236。
//
// 后果是 12 套主题的努力在这两处**全部落空**：
//   · 8 套深色主题下，删除渠道 / 密钥 / 分组 / 代理的确认弹窗是 antd 的
//     浅色默认皮肤，主按钮是默认蓝 #1677ff，而不是本站的陶土色；
//   · message.success 是白色气泡，压在深色页面上很扎眼。
//
// ---- 做法 ----
//
// App.vue 里用 message.useMessage() / Modal.useModal() 取**带主题上下文**的
// 实例（这两个 hook 会把 contextHolder 挂进组件树，因此能读到 ConfigProvider
// 的令牌），再通过 installFeedback() 注入到这里。对外暴露的 message / confirm
// 与 antd 同形，所以调用点只需换一行 import —— 104 处 message.* 与 7 处
// Modal.confirm 一个字都不用改。
//
// ---- 未注入时 ----
//
// 回落到 antd 静态方法：单测、或在 App 之外渲染的组件，行为与改造前逐字一致，
// 不会更差（这条回落是刻意保留的，不是遗漏）。
import { message as antdMessage, Modal } from 'ant-design-vue'
import type { MessageInstance } from 'ant-design-vue/es/message/interface'
import type { ModalFuncProps } from 'ant-design-vue/es/modal/Modal'

/** 与 antd 静态 message 同形（静态对象上多出来的 useMessage / config 不属于这里） */
type MessageApi = MessageInstance

/** 与 antd 的 Modal.confirm 同形：返回可 destroy / update 的句柄 */
type ModalFunc = (props: ModalFuncProps) => {
  destroy: () => void
  update: (configUpdate: ModalFuncProps) => void
}

/**
 * 与 useModal() 的返回同形（静态 Modal 上的 warn 是 warning 的别名，本项目没用过）。
 *
 * 五个方法都要有：除了 confirm，全站还用了 Modal.info（KeysView 的「看不到明文」）、
 * Modal.warning / success / error（ChannelsView 的图标抓取与连通性测试、
 * ProxiesView 的测试失败）—— 它们同样是静态方法，同样掉主题，一并收进来。
 */
type ModalApi = {
  info: ModalFunc
  success: ModalFunc
  error: ModalFunc
  warning: ModalFunc
  confirm: ModalFunc
}

let themedMessage: MessageApi | null = null
let themedModal: ModalApi | null = null

/** 由 App.vue 在挂载时调用一次，注入带主题上下文的实例。 */
export function installFeedback(m: MessageApi, mo: ModalApi): void {
  themedMessage = m
  themedModal = mo
}

/** 未注入时的回落实现（单测、或在 App 之外渲染的组件走这条）。
 *  提到模块级而不是写在 mod() 里：那会在每次调用时重建 5 个闭包。 */
const staticModal: ModalApi = {
  info: (p) => Modal.info(p),
  success: (p) => Modal.success(p),
  error: (p) => Modal.error(p),
  warning: (p) => Modal.warning(p),
  confirm: (p) => Modal.confirm(p)
}

const msg = (): MessageApi => themedMessage ?? (antdMessage as unknown as MessageApi)
const mod = (): ModalApi => themedModal ?? staticModal

/**
 * 与 antd 的 message 同形。调用点写法不变：
 *   import { message } from '@/utils/feedback'
 *   message.success('已保存')
 */
export const message: MessageApi = {
  info: (content, duration, onClose) => msg().info(content, duration, onClose),
  success: (content, duration, onClose) => msg().success(content, duration, onClose),
  error: (content, duration, onClose) => msg().error(content, duration, onClose),
  warning: (content, duration, onClose) => msg().warning(content, duration, onClose),
  loading: (content, duration, onClose) => msg().loading(content, duration, onClose),
  open: (args) => msg().open(args),
  destroy: (key) => msg().destroy(key)
}

/**
 * 与 antd 的 Modal 静态方法同形。用法：
 *   import { modal } from '@/utils/feedback'
 *   modal.confirm({ title: '删除渠道？', okText: '删除', okType: 'danger', onOk })
 */
export const modal: ModalApi = {
  info: (p) => mod().info(p),
  success: (p) => mod().success(p),
  error: (p) => mod().error(p),
  warning: (p) => mod().warning(p),
  confirm: (p) => mod().confirm(withSafeConfirmFocus(p))
}

/**
 * 确认弹窗默认把焦点放在**取消**上，而不是确定（2026-10-11）。
 *
 * ---- 为什么 ----
 *
 * antd 的默认值是 `autoFocusButton: 'ok'`（见 es/modal/ConfirmDialog.js:
 * `props.autoFocusButton || 'ok'`，再按它决定给哪个按钮打 autofocus）。
 * 而全站 7 处 modal.confirm 全是**破坏性**动作：删除渠道 / 密钥 / 分组 / 代理、
 * 清空单价、放弃未保存的改动。焦点落在危险按钮上，意味着键盘用户
 * 「回车触发行内删除图标 → 再一次回车」就完成了删除，中间没有任何缓冲 ——
 * 按住回车（按键重复）也会直接删掉。
 *
 * 放在这一层而不是改 7 个调用点：本文件已经是全站轻提示与确认弹窗的唯一出口
 * （见文件头），一处改动覆盖全部调用点，也不会因为将来新增调用点而漏掉。
 *
 * 调用方显式传了 autoFocusButton 时**不覆盖** —— 有些弹窗的目的就是填一个值
 * （如密钥页的「修改每分钟上限」），那里连确定都不该聚焦，它会自己传 null
 * 并在打开后聚焦那个输入框。
 */
function withSafeConfirmFocus(p: ModalFuncProps): ModalFuncProps {
  if (p.autoFocusButton !== undefined) return p
  return { ...p, autoFocusButton: 'cancel' }
}
