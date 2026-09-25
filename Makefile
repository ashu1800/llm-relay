# ============================================================
#  LLM Relay 构建入口
#
#  用法：make help
#
#  这里的目标都刻意保持「薄」—— 真正的逻辑在各个脚本与 Dockerfile 里，
#  Makefile 只负责把带版本号的编译命令固定下来，免得每次手打 -ldflags
#  时把包路径 llm-relay/internal/version.* 敲错（敲错的后果是静默生效失败：
#  -X 打到一个不存在的变量上不会报错，只会让版本号悄悄变成 dev）。
# ============================================================

.DEFAULT_GOAL := help

# ---- 目录 ----
BACKEND_DIR  := backend
FRONTEND_DIR := frontend
# 前端构建产物交给后端 embed 的位置（backend/internal/web/embed.go 里 embed 的就是它）
WEB_DIST     := $(BACKEND_DIR)/internal/web/dist
# 后端产物目录。相对 backend/ 而言，所以最终落在 backend/bin/llm-relay
BIN_DIR      := bin

# ---- 版本信息 ----
# VERSION 走 backend/scripts/resolve-version.sh：git tag 优先，其次 VERSION 文件。
# ?= 而不是 := 是为了允许命令行覆盖（make build VERSION=0.1.2）。
VERSION ?= $(shell sh $(BACKEND_DIR)/scripts/resolve-version.sh 2>/dev/null)
# 脚本没有任何输出时（既没有 tag，又读不到 VERSION 文件）兜底成 dev。
# 不能留空：空的 -X 值会让注入静默失效，而「看起来正常」的版本号比
# 明确写着 dev 更危险 —— 后者至少诚实地说明「这不是发布产物」。
ifeq ($(strip $(VERSION)),)
VERSION := dev
endif
# 提交短哈希与构建时刻。git/date 不可用时给出 unknown 而不是空 ——
# 一个明确的 unknown 在界面上是线索，空白只会让人以为是渲染出了问题。
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)

# 注入的包路径是 llm-relay/internal/version（**不是** main 包）。
# 变量全名 = Go 包的导入路径 + 变量名，写错一点都不会报错、只会静默失效。
#
# 刻意**不注入 BuildType**：不注入时 internal/version 会自动探测
# （容器外 = source），source 形态禁止一键更新 —— 这正是本地构建该有的
# 行为，免得自己编译的二进制被官方发布产物覆盖。确实要产一个
# 「可自我更新的官方式二进制」时，显式加上：
#   make build GO_LDFLAGS="$(GO_LDFLAGS) -X llm-relay/internal/version.BuildType=binary"
GO_LDFLAGS := -s -w \
	-X llm-relay/internal/version.Version=$(VERSION) \
	-X llm-relay/internal/version.Commit=$(COMMIT) \
	-X llm-relay/internal/version.Date=$(DATE)

.PHONY: help version build build-embed test release-snapshot

# 默认目标：列出所有可用目标
help:
	@echo "LLM Relay 构建目标："
	@echo ""
	@printf '  %-18s %s\n' "make version"          "打印解析出的版本号（git tag 优先，其次 VERSION 文件）"
	@printf '  %-18s %s\n' "make build"            "编译后端（不嵌前端），产物 backend/bin/llm-relay"
	@printf '  %-18s %s\n' "make build-embed"      "先编译前端再拷贝产物并编译后端（完整可发布形态）"
	@printf '  %-18s %s\n' "make test"             "跑后端全部测试（go test ./...）"
	@printf '  %-18s %s\n' "make release-snapshot" "本地跑 goreleaser 快照，产物在 dist/"
	@echo ""
	@echo "当前解析值：VERSION=$(VERSION)  COMMIT=$(COMMIT)  DATE=$(DATE)"
	@echo ""
	@echo "提示：make build 不注入 BuildType，自动探测为 source（禁止一键更新），"
	@echo "      本地编译的产物不会被官方发布覆盖。要产可自我更新的二进制，"
	@echo "      见 Makefile 里 GO_LDFLAGS 上方的注释。"

# 打印版本号。只输出值本身，方便被脚本 $(make version) 直接取用。
version:
	@echo $(VERSION)

# 编译后端（**不**嵌前端）。
# 前端产物缺失时也能编译通过：backend/internal/web/dist 里有 .gitkeep 占位，
# 此时界面会退化成没有静态资源 —— 适合只改后端时快速迭代。
build:
	@echo ">>> 编译后端 VERSION=$(VERSION) COMMIT=$(COMMIT)"
	@cd $(BACKEND_DIR) && CGO_ENABLED=0 go build -trimpath \
		-ldflags "$(GO_LDFLAGS)" \
		-o $(BIN_DIR)/llm-relay ./cmd/server
	@echo ">>> 完成：$(BACKEND_DIR)/$(BIN_DIR)/llm-relay"

# 完整形态：先构建前端，再把 frontend/dist 拷成后端 embed 的目录，最后编译。
# 顺序不能颠倒 —— //go:embed 在**编译期**把目录内容烙进二进制，
# 先编译后拷前端只会得到一个嵌着旧界面的二进制（而且不报任何错）。
build-embed:
	@echo ">>> 构建前端"
	@cd $(FRONTEND_DIR) && npm ci && npm run build
	@echo ">>> 拷贝前端产物到 $(WEB_DIST)"
	@rm -rf $(WEB_DIST)
	@cp -r $(FRONTEND_DIR)/dist $(WEB_DIST)
	@$(MAKE) --no-print-directory build

# 后端测试。发布前 CI 会跑同一条命令（失败即中止发布）。
test:
	@cd $(BACKEND_DIR) && go test ./...

# 本地 goreleaser 快照：产出与正式发布同构的归档，但不打 tag、不推送。
# 这是发布前唯一能在本机验证「归档名/校验和/内嵌文件名」三处契约的办法。
release-snapshot:
	@command -v goreleaser >/dev/null 2>&1 || { \
		echo "未找到 goreleaser，请先安装（任选其一）："; \
		echo "  go install github.com/goreleaser/goreleaser/v2@latest"; \
		echo "  brew install goreleaser        # macOS"; \
		echo "  见 https://goreleaser.com/install/"; \
		exit 1; }
	@echo ">>> goreleaser 快照构建（产物在 dist/，不会推送任何东西）"
	@goreleaser release --snapshot --clean
