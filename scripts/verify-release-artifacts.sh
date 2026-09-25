#!/usr/bin/env bash
# 验证**产物命名契约**：.github/workflows/release.yml 里 goreleaser
# 实际产出的文件名，必须与 backend/internal/update 里拼出来的 URL 完全一致。
#
# 为什么必须实测而不是读配置：这个契约跨了三种语言/模板系统 ——
#   Go 的 fmt.Sprintf / goreleaser 的 Go template / YAML 的折叠块语义。
# 配置读起来「显然对」，但 `.Version` 在一个 snapshot 构建里可能带
# `-SNAPSHOT` 后缀、`.ProjectName` 默认取目录名、折叠块 `>-` 会影响换行 ——
# 任何一处不同，第一次发布时所有平台的下载都会 404，而那时才发现
# 已经晚了（用户正等着第一次更新）。
set -uo pipefail
# 仓库根由脚本自身位置推导（脚本在 scripts/ 下），不写死绝对路径 ——
# 否则换个 checkout 目录或换个人跑就失效了。
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# goreleaser 常装在 ~/go/bin 而 PATH 里没有
export PATH=$PATH:$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin
# .goreleaser.yaml 的 before 钩子会跑 `go mod download -C backend`。
# 国内网络到 proxy.golang.org 是不通的，实测会挂在 dial tcp ... i/o timeout
# 上，于是整个 snapshot 构建失败 —— 那是网络问题而不是配置问题
# （GitHub Actions 里到 proxy.golang.org 是通的，所以 CI 不受影响）。
# 这里用国内可达的镜像源。若你所在网络能直连，改成 direct 也行。
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB=off
cd "$SRC"

if ! command -v goreleaser >/dev/null 2>&1; then
  echo "找不到 goreleaser。安装："
  echo "  go install github.com/goreleaser/goreleaser/v2@latest"
  exit 1
fi

echo "### 1. 配置校验"
if goreleaser check 2>&1 | tail -3; then
  echo "  ✓ 配置通过 goreleaser check"
else
  echo "  ✗ 配置有问题"
  exit 1
fi

echo
echo "### 2. 产出 snapshot（只为看文件名）"
# 在副本里跑：goreleaser 会在仓库根写 dist/，别让它污染工作区。
# 顺带说明：v2 的 release 子命令**没有** --dist 参数（实测报
# "unknown flag: --dist"），也不认 DIST 环境变量，产物就是写在工作目录的
# dist/ 下 —— 所以用工作副本 + 读它自己的 dist/ 是最省事的做法。
WORK=/tmp/gr-work
rm -rf "$WORK"
mkdir -p "$WORK"
rsync -a --exclude '.git' --exclude 'dist' --exclude 'node_modules' \
      --exclude 'deploy/data' "$SRC/" "$WORK/" 2>/dev/null
cd "$WORK"

# backend/internal/web/embed.go 里是 //go:embed all:dist —— 前端产物必须在
# **编译之前**就位，否则 goreleaser 直接报
#   internal/web/embed.go:10:12: pattern all:dist: no matching files found
# release.yml 里专门有一步做这件事（构建前端 → 拷到 backend/internal/web/dist），
# 本地同样补上，否则测的就不是真实的发布路径。
#
# 前端产物直接在**工作区**里构建（那里已经有 node_modules），再拷进副本。
# 在副本里跑 npm ci 会很慢，而且失败时看不出原因。
if [ ! -f "$SRC/frontend/dist/index.html" ]; then
  echo "  · 工作区没有前端产物，先构建…"
  (cd "$SRC/frontend" && npm run build >/tmp/fe-build.log 2>&1) || {
    echo "  ✗ 前端构建失败："; tail -15 /tmp/fe-build.log | sed 's/^/    /'; exit 1; }
fi
rm -rf backend/internal/web/dist
cp -r "$SRC/frontend/dist" backend/internal/web/dist
test -f backend/internal/web/dist/index.html || { echo "  ✗ 前端产物没有就位"; exit 1; }
echo "  · 前端产物已就位（$(find backend/internal/web/dist -type f | wc -l) 个文件）"

if goreleaser release --snapshot --clean --timeout 25m >/tmp/gr.log 2>&1; then
  echo "  ✓ snapshot 构建成功"
else
  echo "  ✗ 构建失败，日志末尾："
  tail -25 /tmp/gr.log | sed 's/^/    /'
  exit 1
fi
# 产物落在 <工作副本>/dist —— goreleaser v2 的 release 子命令不认 DIST
# 环境变量（实测给 DIST=/tmp/gr-snap 后它仍然写本地 dist/），
# 所以直接用它实际写出的路径。
SNAP="$WORK/dist"

echo
echo "### 3. 实际产出的归档名"
ACTUAL=$(find "$SNAP" -maxdepth 3 \( -name '*.tar.gz' -o -name '*.zip' \) | sort)
echo "$ACTUAL" | sed 's|^|    |'
if [ -z "$ACTUAL" ]; then
  echo "  ✗ 没有找到任何归档，构建产物目录："
  find "$SNAP" -maxdepth 2 | head -20 | sed 's/^/    /'
  exit 1
fi

echo
echo "### 4. Go 侧期望的名字（按契约生成）"
cd "$SRC"/backend
cat > internal/update/zz_namecheck_test.go <<'EOF'
package update

import (
	"fmt"
	"testing"
)

func TestPrintArchiveNames(t *testing.T) {
	for _, p := range [][2]string{
		{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"},
	} {
		fmt.Printf("EXPECT %s\n", ArchiveName("0.1.2", p[0], p[1]))
	}
	fmt.Printf("EXPECT checksum %s\n", ChecksumAssetName)
}
EOF
EXPECT=$(go test ./internal/update/ -run TestPrintArchiveNames -v 2>&1 | grep '^EXPECT' | sed 's/^EXPECT //')
rm -f internal/update/zz_namecheck_test.go
echo "$EXPECT" | sed 's|^|    |'

echo
echo "### 5. 逐条比对"
rc=0
# snapshot 版本号是 0.0.0-SNAPSHOT-<sha>，所以只比「形状」：
# 前缀 llm-relay_、平台后缀、扩展名。Go 侧去掉 v 前缀，goreleaser 的
# .Version 本来就不带 v，两边应当一致。
for plat in linux_amd64 linux_arm64 darwin_arm64; do
  if echo "$ACTUAL" | grep -qE "llm-relay_[^/]*_${plat}\.tar\.gz$"; then
    echo "  ✓ $plat 的 tar.gz 命名符合 <name>_<version>_<os>_<arch>.tar.gz"
  else
    echo "  ✗ $plat 找不到符合契约的归档"; rc=1
  fi
done
if echo "$ACTUAL" | grep -qE 'llm-relay_[^/]*_windows_amd64\.zip$'; then
  echo "  ✓ windows 用 .zip（与 Go 侧 BinaryName/解压分支一致）"
else
  echo "  ✗ windows 的 zip 缺失或命名不符"; rc=1
fi

echo
echo "### 6. 校验和文件名"
if [ -f "$SNAP/checksums.txt" ]; then
  echo "  ✓ checksums.txt 存在"
  echo "    格式抽样（应为两列 <hash>  <filename>）:"
  head -2 "$SNAP/checksums.txt" | sed 's/^/      /'
  if head -1 "$SNAP/checksums.txt" | awk '{exit !(NF==2 && length($1)==64)}'; then
    echo "  ✓ 是 sha256 两列格式"
  else
    echo "  ✗ 校验和格式不对（Go 侧按两列解析）"; rc=1
  fi
else
  echo "  ✗ 没有 checksums.txt（Go 侧找不到它会拒绝安装）"; rc=1
fi

echo
echo "### 7. 归档内的可执行文件名"
SAMPLE=$(echo "$ACTUAL" | grep 'linux_amd64.tar.gz' | head -1)
ARCHIVE_LIST=$(tar -tzf "$SAMPLE")   # 存变量，避免 pipefail 下 tar|grep -q 的 SIGPIPE
if [ -n "$SAMPLE" ]; then
  echo "  归档 $(basename "$SAMPLE") 内含："
  printf '%s\n' "$ARCHIVE_LIST" | sed 's/^/      /' | head -12
  if printf '%s\n' "$ARCHIVE_LIST" | grep -qx 'llm-relay'; then
    echo "  ✓ 含可执行文件 llm-relay（Go 侧按这个名字找）"
  else
    echo "  ✗ 归档内没有名为 llm-relay 的可执行文件"; rc=1
  fi
fi

echo
echo "### 8. 归档里的 deploy/ 是否完整"
# install.sh 会读 deploy/llm-relay-updater.service 来注册宿主侧更新器。
# 归档缺了它，那段逻辑只是 warn 后跳过 —— **不报错**，但一键更新永远不可用。
# 这类「缺一个文件就悄悄少一个功能」的缺口必须由脚本守住。
#
# 先把清单存进变量，不要用 `tar | grep -q`：脚本开了 pipefail，
# 而 grep -q 一匹配上就退出，会让还在写的 tar 收到 SIGPIPE 而以 141 结束，
# 于是整条管道判定为失败 —— 明明匹配上了却报「不在归档里」。实测踩过。
ARCHIVE_LIST=$(tar -tzf "$SAMPLE")
for f in Dockerfile docker-compose.yml .env.example install.sh install-bare.sh \
         restore.sh llm-relay-updater.service; do
  if printf '%s\n' "$ARCHIVE_LIST" | grep -qx "deploy/$f"; then
    echo "  ✓ deploy/$f"
  else
    echo "  ✗ deploy/$f 不在归档里"; rc=1
  fi
done

echo
[ $rc -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
exit $rc
