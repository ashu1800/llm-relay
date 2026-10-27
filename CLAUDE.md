# CLAUDE.md

个人自用优先的大模型 API 网关：多协议入站 → 密钥级「模型 → 渠道优先级」故障转移 → 按「渠道 × 模型」计价。
Go 1.25 + Gin + GORM/PostgreSQL 后端，Vue 3 + Vite + Ant Design Vue 4 前端；前端产物
`//go:embed` 进二进制，交付形态是「单个裸二进制 + systemd」。

> 2026-10-26 破坏性重构：分组（ChannelGroup）整体移除。路由顺序由每把密钥的
> 「模型 → 有序候选渠道」决定；轮询 / 最低延迟策略、分组 RPM/TPM、分组日预算
> 随之删除。旧库升级由 store 的一次性回填迁移（密钥按当时渠道权重生成路由）。

## 常用命令

```bash
# 后端（在 backend/ 下）
go build ./...
go test ./...
go test -race ./...          # CI 用这条；只跑单个包时同样建议带 -race
go run ./cmd/server -config ../config.example.yaml

# 前端（在 frontend/ 下）
npm run dev                  # :5173，/api 代理到 :8888
npm run type-check           # vue-tsc --noEmit
npm run check                # 契约 + 主题对比度（不需要浏览器）
npm run test                 # vitest
npm run build                # 构建 + 预压缩
npm run verify               # type-check + check + test + build，改完前端跑这个

# 类型同步（改过 backend/internal/model 之后）
cd backend && tygo generate  # 或 make gen-types；产物 frontend/src/api/generated/model.ts 要提交

# 构建完整二进制
make build-embed             # 先构建前端、拷贝到 backend/internal/web/dist，再编译后端
```

`.sh` / `.py` 验证脚本要在 WSL 里跑；从 Windows PowerShell 调用时用
`scripts/wsl-bash.psm1`（`Invoke-WslScript -Path ...`），不要直接 `bash C:\...`。

## 架构地图

请求链路（改转发逻辑先读这条）：

```
入站协议 → 归一成 OpenAI Chat（通用语，api/protocols.go）
  → 密钥鉴权 + 模型白名单 + 每模型渠道路由解析（api/middleware.go、api/admin.go 的 KeyRoutesOf）
  → 全局并发闸门 → relay.Service.Relay（候选链 = 密钥指定的有序渠道 + 故障转移）
  → convert.UpstreamRequest 按渠道 protocol 转上游格式 → 上游
  → 响应经 Translator 转回入站协议 → 写回客户端
  → finalizeLog（定价快照 + 报文留存 + 异步落库）
```

| 位置 | 职责 |
|---|---|
| `backend/cmd/server/main.go` | 依赖装配、后台循环、优雅关闭（退出预算 3s+1.5s+0.5s） |
| `backend/internal/api/` | 路由与中间件、管理接口、WebSocket 实时推送、静态资源 |
| `backend/internal/relay/service.go` | 故障转移编排：候选循环、冷却、失败记账、共识豁免 |
| `backend/internal/relay/router.go` | 候选查询（按密钥路由的渠道集）、兜底候选合并、可用性优先排序 |
| `backend/internal/relay/forward.go` | 单次上游调用：协议转换、鉴权头、代理选择、超时 |
| `backend/internal/relay/convert/` | 四协议双向编解码，SSE 逐事件改写 |
| `backend/internal/relay/limit.go` | 密钥级 RPM（分组级 RPM/TPM 已随分组移除） |
| `backend/internal/relay/logwriter.go` | 异步日志队列（容量 2048，队列满丢弃并计数） |
| `backend/internal/pricing/engine.go` | 渠道×模型单价、时段倍率、定价快照 |
| `backend/internal/netguard/` | 出站 SSRF 防护（图标抓取、代理测试） |
| `backend/internal/update/` | 版本检测、下载校验、原子自替换、回滚 |
| `frontend/src/views/` | 五个管理页；看板页含请求日志列表（与概览卡共用筛选） |
| `frontend/src/components/RequestLogPanel.vue` | 日志表（列宽测量、实时插行、键盘模型） |
| `frontend/src/composables/useLive.ts` | 全局单条 WebSocket、断线重连、连接状态判定 |

## 硬约定（违反会被 `npm run check` 或测试拦下）

前端：

1. 文字颜色只用 `--text-*`；`--color-*` 只用于填充/边框/图标（非文本对比度阈值不同）。
2. 弹窗/抽屉宽度只用 `utils/modalWidth.ts` 的 `modalWidth(N)`，模板里不许出现字面 `vw`。
3. 字号只用 `var(--font-size-*)`。
4. 轻提示与确认弹窗一律从 `@/utils/feedback` 引入，不要用 antd 静态方法（拿不到主题上下文）。
5. 表格列显隐走 `useColumnVisibility`（状态单例在 `components/logColumnVisibility.ts`）；
   有测量的表要同步改 `v-if`、`scroll.x`、`allocateWidths` 的 keys 三处。
   注：勾选面板 `ColumnSettings.vue` 已于 2026-10-27 随工具栏按钮一并移除，
   现在没有调整入口，表格沿用已持久化的设置。要恢复面板时 `useColumnVisibility`
   与 `logColumnVisibility` 都还在，重建 UI 即可。
6. 移动端形态判断用 `railMode`（`collapsed && !isMobile`），不要用 `collapsed`。
7. 响应式断点只用 640 / 768 / 1024 / 1200 / 1536；900 只作为侧栏折叠的 JS 断点。
8. 组件是逐文件 import 的（只有 `main.ts` 里那批 antd 组件全局注册），新增标签必须 import。
9. 新增主题见 `DEVELOPMENT.md`「新增一套主题的步骤」，六处缺一不可。
10. **判据抽成纯函数 + 单测**：`components/*.ts`（logLive、tableTools、latency、speed、
    restartGate 等）是本仓库的固定做法，别把判定逻辑写在模板或组件内部。

后端：

11. 带 `gorm:"default:..."` 的 bool 字段存不进 `false`（零值会被 GORM 从 INSERT 省掉），
    可写字段的实体定义里**不要**加 default —— 见 `model.ChannelModel.Enabled`。
12. `NOT NULL` 且无默认值的列不能在已有数据的表上交给 AutoMigrate（会生成
    `ADD COLUMN ... NOT NULL` 被 PG 拒绝，应用起不来）；在 `store.migrateLegacySchema`
    里先带默认值建列，再按需 `DROP DEFAULT`。
13. 新增配置项要同时在 `config.applyEnv` 和 `config.envKeys` 补入口，否则部署侧无法调整且无提示。
14. 同一口径只允许一个实现：`relay.UsageTotal` / `relay.ClientKey` / `relay.ChannelDefaultModel`
    等都有注释说明「改口径必须一起改哪几处」。
15. 错误回显不要带库细节：走 `writeInternalError` / `writeConflictOrInternal`。
16. 超时与预算改动要一起看 `main.go` 顶部三个 shutdown 常量（总和必须小于 systemd 的 90s）。
17. 注释解释「为什么」并记录实测依据（数字、复现方式、踩过的坑）；只写「做什么」的注释没有价值。

## 验证顺序

改后端：`go vet ./...` → `go test ./...`（提交前带 `-race`）。
改前端：`npm run verify`。
改协议转换/转发链路：`scripts/verify-all.sh`（WSL，需要运行中的服务与数据库）。
改 UI 观感/交互：`.shots/` 下的脚本（需要带调试端口的 Chrome，不入库）。

## 文档

| 文档 | 内容 |
|---|---|
| `DEVELOPMENT.md` | 实现取舍与实测记录、验证脚本全集、发布流程 —— **改非显而易见的行为前先读** |
| `UI-SPEC.md` | UI 规格（令牌、布局、列宽、动效的实测数据） |
| `README.md` | 使用与部署说明 |
| `deploy/install.sh` | 安装/升级的唯一部署方式 |
