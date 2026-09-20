<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { InputNumber, message, Modal } from 'ant-design-vue'
import {
  PlusOutlined,
  ReloadOutlined,
  DeleteOutlined,
  CopyOutlined,
  EditOutlined,
  KeyOutlined,
  DashboardOutlined,
  StopOutlined,
  PlayCircleOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import GroupTag from '@/components/GroupTag.vue'
import { writeClipboard } from '@/utils/clipboard'
import { fmtTime } from '@/utils/fmtTime'

import type { APIKey, ChannelGroup } from '@/api/types'

const loading = ref(false)
const rows = ref<APIKey[]>([])
const groups = ref<ChannelGroup[]>([])
const modalOpen = ref(false)
const saving = ref(false)
const createdKey = ref('')
const editing = ref<APIKey | null>(null)

const form = reactive({
  name: '',
  rate_limit_rpm: 0,
  enabled: true,
  // 白名单为空数组表示不限制，与后端 StringList 的语义一致
  allowed_models: [] as string[],
  allowed_groups: [] as string[]
})

const title = computed(() => (editing.value ? '编辑密钥' : '新建密钥'))

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为密钥本来就没有
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<{ items: APIKey[] }>('/keys')
    rows.value = res.items || []
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

// 分组白名单条目的解析：条目统一是分组 ID（保存时后端归一化），但老数据、
// 导入的备份里可能还是名字 —— 数字先按 ID 找，找不到再按名字找一次，
// 都找不到的就是悬空引用（分组已删），界面上要能看出来而不是显示一个裸 ID
function groupRefInfo(ref: string): { label: string; color?: string; missing: boolean } {
  const g =
    groups.value.find((x) => String(x.id) === ref) ?? groups.value.find((x) => x.name === ref)
  if (g) return { label: g.name, color: g.color, missing: false }
  return { label: ref, missing: true }
}

// 密钥明文：按需从后端解密，取到后缓存在内存里。
// 缓存是刻意的 —— 悬停和点击复制是同一个诉求的两种触发方式，
// 不缓存的话鼠标扫过列表就会打出一串解密请求。
interface RevealResult {
  available: boolean
  key?: string
  reason?: string
}
const revealed = reactive<Record<number, RevealResult | 'loading'>>({})

async function ensureKey(record: APIKey): Promise<RevealResult | null> {
  const cached = revealed[record.id]
  if (cached && cached !== 'loading') return cached
  if (cached === 'loading') return null
  revealed[record.id] = 'loading'
  try {
    const res = await api.get<RevealResult>('/keys/' + record.id + '/reveal')
    revealed[record.id] = res
    return res
  } catch (e: any) {
    const fail: RevealResult = { available: false, reason: e.message || '读取失败' }
    revealed[record.id] = fail
    return fail
  }
}

function tooltipOf(record: APIKey) {
  const info = revealed[record.id]
  if (info === 'loading') return '读取中…'
  if (!info) return '悬停读取完整密钥，点击复制'
  if (!info.available) return info.reason || '明文不可用'
  return info.key + '（点击复制）'
}

// 写剪贴板的实现提取到了 utils/clipboard.ts（日志详情的 Trace ID 复制
// 也要用）：降级路径只维护一份，别处出问题只改一处。

async function copyRowKey(record: APIKey) {
  const info = await ensureKey(record)
  if (!info) return
  if (!info.available || !info.key) {
    // 看不到明文不是错误，是一种正常状态（升级前创建的密钥只存过哈希）
    Modal.info({ title: '看不到这把密钥的明文', content: info.reason || '明文不可用', width: 460 })
    return
  }
  if (await writeClipboard(info.key)) message.success('已复制完整密钥')
  else message.warning('复制失败，请在悬浮提示里手动选择复制')
}

// 客户端该填的调用地址 —— 中转端点和这个后台同进程、同端口（见后端 Register 里的
// /v1 与 /v1beta 路由分组），所以它就是当前访问的这个源加上 /v1。
//
// 不写死 127.0.0.1:8888：端口可能改过，也可能从别的机器用 IP 打开这个后台，
// 写死的地址复制走就是错的。带 /v1 是因为它要填进各家客户端的 Base URL 输入框，
// 客户端会把 /chat/completions 接在它后面。
const baseUrl = window.location.origin + '/v1'

async function copyBaseUrl() {
  // 与复制密钥共用 writeClipboard：http 下 clipboard API 不可用、
  // 以及 writeText 被挂起等授权这两种情况它都处理过了
  if (await writeClipboard(baseUrl)) message.success('已复制 BaseURL')
  else message.warning('复制失败，请手动选择复制')
}

async function loadGroups() {
  try {
    const res = await api.get<{ items: ChannelGroup[] }>('/groups')
    groups.value = res.items || []
  } catch (e: any) {
    message.error('分组候选加载失败：' + e.message)
  }
}

function openCreate() {
  editing.value = null
  form.name = ''
  form.rate_limit_rpm = 0
  form.enabled = true
  form.allowed_models = []
  form.allowed_groups = []
  createdKey.value = ''
  modalOpen.value = true
}

function openEdit(row: APIKey) {
  editing.value = row
  form.name = row.name
  form.rate_limit_rpm = row.rate_limit_rpm
  form.enabled = row.enabled
  form.allowed_models = [...(row.allowed_models || [])]
  // 白名单条目换成分组 ID（受控多选的值）：库里老条目可能是名字，
  // 能解析就换，解析不了的（悬空引用）原样带着 —— 保存时后端会报错指出它
  form.allowed_groups = (row.allowed_groups || []).map((ref) => {
    const g =
      groups.value.find((x) => String(x.id) === ref) ??
      groups.value.find((x) => x.name === ref)
    return g ? String(g.id) : ref
  })
  createdKey.value = ''
  modalOpen.value = true
}

// 限流额度的展示：0 表示跟随全局默认，负数表示不限
function limitText(v: number) {
  if (v === 0) return '跟随全局默认'
  if (v < 0) return '不限'
  return v + ' 次/分钟'
}

// 白名单展示：空表示不限制
function whitelistText(v: string[] | null) {
  if (!v || v.length === 0) return '不限'
  return v.join('、')
}

// tags 模式可以自由输入，提交前去空白、去重
function cleanList(v: string[]) {
  return Array.from(new Set((v || []).map((s) => String(s).trim()).filter(Boolean)))
}

function setLimit(row: APIKey) {
  let input = String(row.rate_limit_rpm)
  Modal.confirm({
    centered: true,
    title: '设置每分钟请求上限 · ' + row.name,
    content: () =>
      h('div', [
        h('p', { style: 'font-size:12px;color:var(--color-text-secondary);margin-bottom:8px' }, [
          '填 0 表示跟随全局默认，填负数表示这把密钥完全不限流（适合本地压测）。'
        ]),
        // antd 的 InputNumber 组件类型与 h() 的重载对不上（改动前就存在的报错），
        // 这里显式断言绕开类型检查，运行时行为不变
        h(InputNumber as any, {
          defaultValue: row.rate_limit_rpm,
          min: -1,
          max: 1000000,
          style: 'width:100%',
          'onUpdate:value': (v: number) => {
            input = String(v ?? 0)
          }
        })
      ]),
    async onOk() {
      const n = parseInt(input, 10)
      if (!isFinite(n) || n < -1) {
        message.warning('请填 0 或正整数，-1 表示不限')
        return
      }
      try {
        await api.put('/keys/' + row.id, { rate_limit_rpm: n })
        message.success('已更新')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

// onCancel 处理「取消 / 再建一把」：已经看到明文时，这个按钮的语义是
// 「再建一把」—— 清掉明文并重置表单，弹窗留着；其余情况就是普通关闭
function onCancel() {
  if (createdKey.value) {
    createdKey.value = ''
    form.name = ''
    form.rate_limit_rpm = 0
    form.allowed_models = []
    form.allowed_groups = []
    form.enabled = true
    return
  }
  closeModal()
}

// closeModal 关掉弹窗并把「刚建出来的明文」清掉，下次打开是干净的表单
function closeModal() {
  modalOpen.value = false
  createdKey.value = ''
}

async function save() {
  // 刚创建完、弹窗还开着（在展示明文）时，确定按钮的语义是「完成」而不是
  // 「再建一把」。原来它一直绑着创建动作，用户看完明文顺手点确定，
  // 就会静默建出第二把一模一样的密钥 —— 而且可以无限点下去。
  if (createdKey.value) {
    closeModal()
    return
  }
  if (!form.name.trim()) {
    message.warning('名称必填')
    return
  }
  saving.value = true
  try {
    // 白名单始终显式发送：空数组是有效值（表示清空），不传则后端保持原值
    const body = {
      name: form.name.trim(),
      rate_limit_rpm: form.rate_limit_rpm,
      allowed_models: cleanList(form.allowed_models),
      allowed_groups: cleanList(form.allowed_groups)
    }
    if (editing.value) {
      await api.put('/keys/' + editing.value.id, { ...body, enabled: form.enabled })
      message.success('已更新')
      closeModal()
      await load()
    } else {
      const res = await api.post<{ key: string }>('/keys', { ...body, enabled: form.enabled })
      // 明文留在弹窗里方便立刻复制走（列表里也随时能看，见 ensureKey）
      createdKey.value = res.key
      await load()
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

async function copyKey() {
  if (await writeClipboard(createdKey.value)) message.success('已复制到剪贴板')
  else message.warning('复制失败，请手动选择复制')
}

async function toggle(row: APIKey) {
  try {
    await api.put('/keys/' + row.id, { enabled: !row.enabled })
    await load()
  } catch (e: any) {
    message.error(e.message)
  }
}

function confirmDelete(row: APIKey) {
  Modal.confirm({
    centered: true,
    title: '确认删除密钥',
    content: '使用该密钥的客户端将立即无法调用。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/keys/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

function fmt(t: string | null) {
  if (!t) return '从未使用'
  // 与全站统一口径（原来 toLocaleString 出来是斜杠分隔、月日不补零）
  return fmtTime(t)
}

onMounted(() => {
  load()
  loadGroups()
})
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建密钥</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <!-- 客户端该填的地址放在这里：建完密钥紧接着就是把它填进客户端，
             而这一页原来只在列密钥，地址得去别处找 -->
        <a-tooltip title="点击复制这个地址">
          <span class="baseurl-pill" @click="copyBaseUrl">
            <span class="baseurl-label">BaseURL</span>
            <span class="baseurl-text">{{ baseUrl }}</span>
            <CopyOutlined class="baseurl-copy" />
          </span>
        </a-tooltip>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">共 {{ rows.length }} 个密钥</span>
      </div>

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="密钥列表加载失败"
        @retry="load"
      >
      <!-- scroll.x 必须不小于各列宽度之和（名称 150 + 密钥 170 + 模型白名单 160
           + 分组白名单 180 + 最后使用 150 + 限流 130 + 状态 90 + 操作 132 = 1162）：
           声明偏小时右侧固定的「操作」列会盖住左边最后一列，
           表现为表头被截断、单元格内容被压住，而且不报错。
           原来写的是 1170、少了 60（那是四列宽度还没跟着改的时候）。
           操作 200 -> 132 是图标按钮那一次，见下方操作列上方的注释。
           核对脚本：scripts/check-table-widths.mjs -->
      <a-table :data-source="rows" :loading="loading" :pagination="false" row-key="id" size="small" :scroll="{ x: 1162 }">
        <template #emptyText>
          <a-empty description="还没有密钥，点「新建密钥」创建第一个" />
        </template>
        <a-table-column title="名称" data-index="name" :width="150" />
        <a-table-column title="密钥" :width="170">
          <template #default="{ record }">
            <!-- 胶囊 + 悬停看全量 + 点击复制：三个动作都指向同一件事
                 「我要把这把密钥拿去用」。明文按需从后端解密，不随列表下发 -->
            <a-tooltip
              placement="topLeft"
              :title="tooltipOf(record)"
              @open-change="(open: boolean) => open && ensureKey(record)"
            >
              <span class="key-pill" @click="copyRowKey(record)">
                <KeyOutlined />
                <span class="key-text">{{ record.key_prefix }}…</span>
                <CopyOutlined class="key-copy" />
              </span>
            </a-tooltip>
          </template>
        </a-table-column>
        <a-table-column title="模型白名单" :width="160" ellipsis>
          <template #default="{ record }">
            <span v-if="whitelistText(record.allowed_models) === '不限'" class="muted">不限</span>
            <span v-else>{{ whitelistText(record.allowed_models) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="分组白名单" :width="180" ellipsis>
          <template #default="{ record }">
            <span v-if="!(record.allowed_groups || []).length" class="muted">不限</span>
            <span v-else class="group-tag-list">
              <!-- 条目是分组 ID（颜色与名字去分组表里解析），与分组管理、
                   渠道列表用的是同一份颜色。悬空引用（分组已删）单独标出来：
                   那种条目会让这把密钥调用时 403，不能只显示一个裸 ID -->
              <template v-for="ref in record.allowed_groups" :key="ref">
                <GroupTag
                  v-if="!groupRefInfo(ref).missing"
                  :name="groupRefInfo(ref).label"
                  :color="groupRefInfo(ref).color"
                />
                <a-tooltip v-else :key="ref + '-missing'" :title="'引用的分组已不存在：' + ref + '（这会让该密钥调用时被拒，请编辑密钥清掉它）'">
                  <span class="group-ref-missing">{{ ref }}</span>
                </a-tooltip>
              </template>
            </span>
          </template>
        </a-table-column>
        <a-table-column title="最后使用" :width="150">
          <template #default="{ record }">{{ fmt(record.last_used_at) }}</template>
        </a-table-column>
        <a-table-column title="限流" :width="130">
          <template #default="{ record }">{{ limitText(record.rate_limit_rpm) }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="90">
          <template #default="{ record }">
            <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
          </template>
        </a-table-column>
        <!-- 四个动作改成图标按钮，与渠道页同一个写法（.table-icon-btn）。
             原来四个「图标 + 文字」链接实测占 181px，列宽因此要 200；
             图标按钮每个 28px，四个加间距共 124px，列宽 132 就够，
             表格总宽从 1230 降到 1162 —— 1440 与 1334 两个视口下都能完整装进
             容器，横向滚动消失，也不再出现「固定列悬停提示被裁掉一截」。

             四个动作里有两个不是自解释的：
               · 「改限额」—— 图标只能是仪表盘/速度一类，看不出改的是「每分钟上限」
               · 「启用/停用」—— 同一个按钮两种语义，图标必须跟着 :checked 换
             这两处靠 tooltip 补名字，其余两个（编辑、删除）本就是通用约定。 -->
        <a-table-column title="操作" :width="132" fixed="right">
          <template #default="{ record }">
            <a-space :size="4">
              <a-tooltip title="编辑密钥：名称、白名单">
                <a-button
                  class="table-icon-btn"
                  type="text"
                  size="small"
                  :aria-label="'编辑密钥 ' + record.name"
                  @click="openEdit(record)"
                >
                  <EditOutlined />
                </a-button>
              </a-tooltip>
              <a-tooltip title="改限额：每分钟请求数上限">
                <a-button
                  class="table-icon-btn"
                  type="text"
                  size="small"
                  :aria-label="'修改 ' + record.name + ' 的每分钟请求上限'"
                  @click="setLimit(record)"
                >
                  <DashboardOutlined />
                </a-button>
              </a-tooltip>
              <a-tooltip :title="record.enabled ? '停用：使用该密钥的客户端立即失效' : '启用：恢复这把密钥的调用'">
                <a-button
                  class="table-icon-btn"
                  type="text"
                  size="small"
                  :aria-label="(record.enabled ? '停用密钥 ' : '启用密钥 ') + record.name"
                  @click="toggle(record)"
                >
                  <!-- 图标跟着状态换，不只换颜色：色觉障碍下也要能分辨
                       「这一下会把它关掉」还是「会把它打开」 -->
                  <StopOutlined v-if="record.enabled" />
                  <PlayCircleOutlined v-else />
                </a-button>
              </a-tooltip>
              <a-tooltip title="删除密钥">
                <a-button
                  class="table-icon-btn is-danger"
                  type="text"
                  size="small"
                  :aria-label="'删除密钥 ' + record.name"
                  @click="confirmDelete(record)"
                >
                  <DeleteOutlined />
                </a-button>
              </a-tooltip>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
      </DataState>
    </section>

    <a-modal
      v-model:open="modalOpen"
      :title="title"
      :confirm-loading="saving"
      :width="'min(600px, 94vw)'"
      :ok-text="createdKey ? '完成' : '确定'"
      :cancel-text="createdKey ? '再建一把' : '取消'"
      centered
      @ok="save"
      @cancel="onCancel"
    >
      <a-form layout="vertical">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" placeholder="例如 本地客户端" />
        </a-form-item>
        <a-form-item label="每分钟请求上限">
          <a-input-number v-model:value="form.rate_limit_rpm" :min="-1" :max="1000000" style="width: 100%" />
          <div class="field-hint">0 表示跟随全局默认；负数表示这把密钥完全不限流。</div>
        </a-form-item>
        <a-form-item label="允许调用的模型">
          <a-select
            v-model:value="form.allowed_models"
            mode="tags"
            :token-separators="[',', '，']"
            placeholder="输入模型名后回车，可填多个"
          />
          <div class="field-hint">留空表示不限制；填了则只有列表内的模型可以被这把密钥调用。</div>
        </a-form-item>
        <a-form-item label="允许使用的分组">
          <!-- 受控多选而不是自由输入的 tags：值是分组 ID，选项就是现有分组。
               以前存名字 + 允许任意输入，打错的名字静默入库，分组一改名
               还会把引用它的密钥全体打断 —— 现在保存前就在这份选项里选 -->
          <a-select
            v-model:value="form.allowed_groups"
            mode="multiple"
            placeholder="选择允许使用的分组"
          >
            <a-select-option v-for="g in groups" :key="g.id" :value="String(g.id)">{{ g.name }}</a-select-option>
          </a-select>
          <div class="field-hint">留空表示不限制。按分组记录、不受分组改名影响；引用的分组被删除时调用会被拒绝并提示。</div>
        </a-form-item>
        <a-form-item label="启用">
          <a-switch v-model:checked="form.enabled" />
        </a-form-item>
      </a-form>

      <a-alert
        v-if="createdKey"
        type="success"
        show-icon
        message="密钥创建成功"
        description="密钥已保存。以后随时能在列表里悬停查看、点击复制，这里也可以直接复制走。"
        style="margin-top: 8px"
      />
      <div v-if="createdKey" class="key-box">
        <code>{{ createdKey }}</code>
        <a-button size="small" @click="copyKey"><CopyOutlined /> 复制</a-button>
      </div>
    </a-modal>
  </div>
</template>

<style scoped>
.manage-container { padding: var(--gap); }
.manage-panel { padding: 0; overflow: hidden; }
.manage-toolbar { display: flex; align-items: center; gap: var(--gap); padding: var(--gap); min-height: 64px; }
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.toolbar-left { display: flex; gap: var(--gap); }
.toolbar-spacer { flex: 1; }
.toolbar-hint { color: var(--color-text-secondary); font-size: 13px; }
/* BaseURL 胶囊：与密钥胶囊同一套视觉语言，但走中性色 —— 它只是陈列一条
   可以复制走的地址，用主色会和旁边的「新建密钥」抢注意力 */
.baseurl-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  max-width: 340px;
  padding: 3px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-bg);
  font-size: 12px;
  line-height: 20px;
  cursor: var(--cursor-hand);
  transition: border-color 0.2s ease;
}
.baseurl-pill:hover { border-color: var(--color-primary); }
.baseurl-label { flex: none; color: var(--color-text-secondary); }
/* 地址是代码类内容，等宽便于逐段核对；太长时省略号收尾，不撑破工具栏 */
.baseurl-text {
  font-family: var(--font-family-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.baseurl-copy { flex: none; opacity: 0.55; transition: opacity 0.2s ease; }
.baseurl-pill:hover .baseurl-copy { opacity: 1; color: var(--color-primary); }
.muted { color: var(--color-text-secondary); }
/* 密钥胶囊：与分组胶囊同一套视觉语言（浅底 + 圆角 + 同色文字），
   但用等宽字体 —— 密钥是代码类内容，逐字符比对时等宽好读得多 */
.key-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  padding: 1px 8px;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-primary) 13%, transparent);
  /* 13% 主色底上主色文字只有 2.89:1；ink 版 5.17:1。
     底色仍留在主色系里，观感不变。 */
  color: var(--text-primary-ink);
  font-family: var(--font-family-mono);
  font-size: 12px;
  line-height: 18px;
  cursor: var(--cursor-hand);
  transition: background 0.2s ease;
}
.key-pill:hover { background: color-mix(in oklab, var(--color-primary) 22%, transparent); }
.key-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.key-copy { opacity: 0; transition: opacity 0.2s ease; }
.key-pill:hover .key-copy { opacity: 0.75; }

.group-tag-list { display: inline-flex; flex-wrap: wrap; gap: 4px; }
/* 悬空的分组引用：分组已删、条目还在。用红字虚线框与正常胶囊区分 ——
   它不是一种正常的白名单状态，留着它这把密钥调用时会被整体拒绝 */
.group-ref-missing {
  display: inline-block;
  padding: 1px 8px;
  border: 1px dashed var(--color-red, #cf1322);
  border-radius: var(--radius-control);
  color: var(--color-red, #cf1322);
  font-size: 12px;
  line-height: 20px;
  cursor: help;
}
.key-box {
  display: flex;
  align-items: center;
  gap: var(--gap);
  margin-top: 8px;
  padding: 8px;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-control);
  word-break: break-all;
}
.key-box code { flex: 1; font-family: var(--font-family-mono); font-size: 12px; }
</style>

