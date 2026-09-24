// 表单提交前的那一次统一校验 —— 四个视图（渠道 / 密钥 / 分组 / 代理）共用。
//
// 为什么需要它，而不是各页各写一段：
// 2026-09-24 的 UI 审评发现，这四张表单原先都**没有接线** ——
// 字段上的 `required` 只是个视觉星号，a-form 没绑 :model、字段没绑 name、
// 表单没绑 :rules，唯一的把关是 save() 开头一句
// `if (!form.name.trim()) { message.warning('名称必填'); return }`。
// 那句话有三个问题：
//   1. 一次只报一个字段（名称填好后才发现地址也空着，要来回试两轮）；
//   2. 提示是屏幕中上方一闪而过的 message，不落在出错的那个输入框旁边 ——
//      表单长到需要滚动时（渠道表单十来段），用户根本不知道说的是哪一个；
//   3. 读屏软件读不到这种关联，报错与字段之间没有任何可访问的联系。
// 接线之后由 a-form 统一负责：失焦即时校验、错误显示在字段下方、
// 提交时一次把全部问题摆出来。
//
// 这个 composable 就是「提交时校验」的那一步：
//   - 走 formRef.validate()，拿到**全部**出错字段而不是第一个；
//   - 失败时把视野与焦点一起带到第一个出错的字段。
import type { Ref } from 'vue'
import type { FormInstance } from 'ant-design-vue/es/form'

/** 校验失败时抛出的 errorFields 里，一项的形状（antd 没有导出这个类型） */
type ErrorField = { name: (string | number)[]; errors: string[] }

export function useFormValidate(formRef: Ref<FormInstance | undefined>) {
  /**
   * 通过返回 true；不通过返回 false（调用方据此 return，不要继续提交）。
   *
   * 失败时做两件事，缺一不可：
   *   scrollToField —— 表单比弹窗高时，出错的字段可能在视野外。
   *     antd 有 `scrollToFirstError` 属性，但它挂在 **finishFailed** 上，
   *     而 finishFailed 只在原生表单提交（点 type=submit 的按钮 / 回车）时触发；
   *     这四个弹窗的确定按钮走的是 Modal 的 @ok → save()，不经过原生提交，
   *     所以那条路不会走。这一点是读源码确认的（ant-design-vue/es/form/Form.js
   *     的 handleFinishFailed），不是靠试出来的。
   *   focus —— 光滚动不聚焦，键盘与读屏用户仍然停在原来的按钮上，
   *     既不知道该改哪里，也拿不到字段自己的 aria-describedby 关联。
   *     这是**实测**的：只调 scrollToField 时，提交后
   *     document.activeElement 仍是确定按钮所在的 DIV。
   */
  async function validateForm(): Promise<boolean> {
    try {
      await formRef.value?.validate()
      return true
    } catch (e: unknown) {
      const first = (e as { errorFields?: ErrorField[] })?.errorFields?.[0]
      if (first?.name) {
        formRef.value?.scrollToField(first.name)
        focusField(first.name)
      }
      return false
    }
  }

  /**
   * 把焦点移到出错字段的输入控件上。
   *
   * 为什么不能只靠 scrollToField：它内部只调用 scrollIntoView（见
   * ant-design-vue/es/form/Form.js 的 scrollToField 实现），不碰焦点。
   *
   * 定位方式：先由字段名在**出错的那些 FormItem 里**找出对应的那一个，
   * 再取它内部第一个可聚焦控件。匹配用的是字段名在控件 `name` / `id`
   * 属性上的出现 —— 比按标签文字或 DOM 顺序更稳（label 可能是插槽里的
   * 复杂结构，例如渠道表单每个标签都带一个 ⓘ tooltip）。
   * 一个都匹配不上就退回第一个出错的 FormItem：反正那也是要改的那个。
   * 全程找不到就静默跳过 —— 聚焦是增强，不该因为它让提交流程报错。
   */
  function focusField(name: (string | number)[]) {
    // 等一帧：错误提示是校验失败后才渲染的，此刻 DOM 里可能还没有
    // .ant-form-item-has-error，直接查会落空
    requestAnimationFrame(() => {
      const key = String(name[0])
      // 只在**当前可见的弹窗**里找。不限定范围的话，页面上若同时存在
      // 别的 .ant-form-item-has-error（例如另一个已关闭但仍在 DOM 里的弹窗，
      // 或渠道页那个独立的「测试模型」内嵌表单），就会把焦点送错地方 ——
      // 而错误的焦点比没有焦点更难排查。取最后一个可见弹窗：
      // antd 的 Modal 是后开的在后面（z-index 更高）。
      const modals = Array.from(document.querySelectorAll<HTMLElement>('.ant-modal-content'))
      const scope = modals.length ? modals[modals.length - 1] : document.body
      const items = Array.from(scope.querySelectorAll<HTMLElement>('.ant-form-item-has-error'))
      if (!items.length) return
      let target: HTMLElement | undefined
      for (const item of items) {
        // 字段名会出现在内部控件的 name/id 上，按它匹配最可靠
        // （antd 给 FormItem 打的 name 属性在包裹层上不一定有）
        if (item.querySelector(`[name="${CSS.escape(key)}"], #${CSS.escape(key)}`)) {
          target = item
          break
        }
      }
      // 没匹配上就用第一个出错的字段 —— 反正它也是要改的那个
      const hit = target || items[0]
      const el = hit.querySelector<HTMLElement>(
        'input:not([type="hidden"]), textarea, select, .ant-select-selector, .ant-picker'
      )
      el?.focus()
    })
  }

  return { validateForm }
}
