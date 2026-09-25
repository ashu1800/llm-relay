#!/usr/bin/env bash
# ============================================================
#  release.sh —— 发布一个新版本（本仓库唯一的「交付」动作）
#
#  用法：
#    bash scripts/release.sh patch        # 0.1.0 → 0.1.1（默认）
#    bash scripts/release.sh minor        # 0.1.0 → 0.2.0
#    bash scripts/release.sh major        # 0.1.0 → 1.0.0
#    bash scripts/release.sh v0.1.5       # 显式指定版本（带不带 v 均可）
#    bash scripts/release.sh --dry-run    # 只打印将要做的事
#    bash scripts/release.sh patch --yes  # 跳过确认（供 CI / 无人值守）
#
#  # 为什么这个脚本是唯一的交付动作
#
#  项目支持自动更新，且部署形态是「裸二进制 + systemd」：
#  服务器上的进程通过 GitHub Release 下载归档 → 校验 → 原子替换自己 →
#  systemd（Restart=always）用新文件拉起。这条链路上**没有任何人工部署**。
#  人要做的只剩两件事：把代码推上 main、把版本号命名出来（打 tag）。
#  本脚本把这两件事与「发布真的成功了吗」的验证串成一条命令。
#
#  # 步骤（顺序即依赖，每步失败即停）
#
#    1. 前置检查     在 main、工作区干净、gh 已登录、远端可达
#    2. 本地预检     go vet / go test / 前端 type-check + contracts
#                    （release.yml 会再跑一遍测试；这里先跑是把失败拦在
#                     打 tag 之前 —— tag 一旦推上去就是「已发布」状态）
#    3. 计算版本号   从 git tag 取最新 semver 按参数递增
#    4. 推送 main    tag 必须指向远端已有的提交
#    5. 打 tag 推送  触发 .github/workflows/release.yml
#    6. 观察流水线   gh run watch 直到全部 job 结束
#    7. 验证产物     Release 归档 + checksums.txt 在场（镜像为可选项）
#
#  # 部署形态与产物的关系
#
#  release.yml 产出两种东西：
#    · goreleaser 归档（binary 通道用）：裸机 / systemd 部署的一键更新来源，
#      命名契约 llm-relay_<version>_<goos>_<goarch>.tar.gz 必须严格保持，
#      更新器按它拼下载 URL。
#    · GHCR 镜像（docker 通道用）：docker 形态部署的更新来源。
#    两者都在流水线里构建；本脚本只验证 binary 通道必需的归档与校验和，
#    镜像可达性作为附加检查（失败只警告，因为 docker 形态不是当前主用形态）。
#
#  # 失败语义
#
#  每一步失败都会说明「卡在哪、为什么、怎么补救」。第 5 步之后的失败
#  意味着 tag 已推、Release 可能已创建半成品 —— 那是需要人介入的状态，
#  脚本会把补救命令直接打出来。
#
#  # 刻意不做的事
#
#  · 不自动 commit 业务代码。提交什么、何时提交由人决定；工作区不干净时
#    脚本拒绝运行 —— 发布一个自己也说不清包含什么的状态违背版本号的意义。
#  · 不改 VERSION 文件。release.yml 会把 tag 写进 VERSION 并回写 main，
#    本地先写只会在 tag 之后留下一个不属于任何版本的脏改动。
#  · 不部署、不登录服务器。服务器通过自动更新体系自己拿新版本。
# ============================================================
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

REPO="ashu1800/llm-relay"
IMAGE_NAME="ghcr.io/ashu1800/llm-relay"
BRANCH="main"

log()  { printf '\033[1;36m[release]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[release]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[release]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[release]\033[0m %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,60p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2; }

# ---------------------------------------------------------------
# 参数解析
# ---------------------------------------------------------------
DRY_RUN=0
ASSUME_YES=0
BUMP=""
for arg in "$@"; do
  case "$arg" in
    --dry-run|-n)   DRY_RUN=1 ;;
    --yes|-y)       ASSUME_YES=1 ;;
    patch|minor|major) BUMP="$arg" ;;
    -h|--help)      usage ;;
    v[0-9]*|[0-9]*) BUMP="$arg" ;;
    *) die "无法识别的参数：$arg（bash scripts/release.sh -h 查看用法）" ;;
  esac
done
BUMP="${BUMP:-patch}"

# ---------------------------------------------------------------
# 1. 前置检查
# ---------------------------------------------------------------
step() { log "── $*"; }
step "前置检查"

command -v gh >/dev/null 2>&1 \
  || die "未找到 gh CLI（观察发布流水线用）。安装：winget install GitHub.cli"

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "当前目录不是 git 仓库"

CURRENT_BRANCH="$(git branch --show-current)"
[ "$CURRENT_BRANCH" = "$BRANCH" ] || die "当前在 $CURRENT_BRANCH 分支，发布必须从 $BRANCH 出"

# 工作区干净 = 「这个 tag 里到底是什么」有明确答案。
# 未跟踪文件（截图、临时脚本）不影响发布内容，不拦。
DIRTY="$(git status --porcelain --untracked-files=no)"
[ -z "$DIRTY" ] || { echo "$DIRTY" | sed 's/^/    /'; die "有未提交改动（见上）。先提交再发布"; }

gh auth status >/dev/null 2>&1 || die "gh 未登录。运行：gh auth login"

git ls-remote --exit-code origin "$BRANCH" >/dev/null 2>&1 \
  || die "远端 origin 上没有 $BRANCH（或网络不通）"

# 与远端对齐再继续：本地落后时打出的 tag 会缺提交
git fetch origin "$BRANCH" --quiet
BEHIND="$(git rev-list --count "$CURRENT_BRANCH..origin/$BRANCH")"
[ "$BEHIND" -eq 0 ] || die "本地 $BRANCH 落后远端 $BEHIND 个提交。先 git pull --rebase"

# 远端必须有 release.yml：本地领先远端、还没把它推上去时，
# tag 推上去也触发不了发布 —— 那会表现为「tag 在而流水线静默缺失」。
git cat-file -e "$BRANCH:.github/workflows/release.yml" 2>/dev/null \
  || die "当前提交里没有 .github/workflows/release.yml（发布流水线不存在），无法发布"

ok "在 $BRANCH，工作区干净，gh 已登录，流水线在场"

# ---------------------------------------------------------------
# 2. 本地预检
# ---------------------------------------------------------------
step "本地预检"

if [ "$DRY_RUN" = "1" ]; then
  log "（dry-run 跳过测试）"
else
  log "后端 vet + test…"
  ( cd backend && GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" GOSUMDB=off go vet ./... ) \
    || die "go vet 未通过"
  ( cd backend && GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" GOSUMDB=off go test ./... ) \
    || die "go test 未通过"

  log "前端 type-check + 契约检查…"
  ( cd frontend && npm run type-check >/tmp/release-typecheck.log 2>&1 ) \
    || { tail -25 /tmp/release-typecheck.log; die "前端类型检查未通过"; }
  ( cd frontend && npm run check >/tmp/release-contracts.log 2>&1 ) \
    || { grep -E 'FAIL|未通过' /tmp/release-contracts.log | head -25; die "前端契约检查未通过"; }

  ok "本地预检通过"
fi

# ---------------------------------------------------------------
# 3. 计算版本号
# ---------------------------------------------------------------
step "计算版本号"

# 只统计 v + 三段数字的 tag；sort -V 而不是字符串排序
# （0.1.10 在字符串排序里会排在 0.1.9 前面，导致版本号算倒）。
LATEST_TAG="$(git tag -l 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' | sort -V | tail -1 || true)"

case "$BUMP" in
  patch|minor|major)
    if [ -z "$LATEST_TAG" ]; then
      BASE="$(tr -d '\r\n' < backend/internal/version/VERSION 2>/dev/null || echo 0.1.0)"
      log "仓库还没有 tag，以 VERSION 文件的 $BASE 为基线"
    else
      BASE="${LATEST_TAG#v}"
    fi
    IFS='.' read -r MAJOR MINOR PATCH <<< "$BASE"
    case "$BUMP" in
      major) MAJOR=$((MAJOR+1)); MINOR=0;  PATCH=0 ;;
      minor) MINOR=$((MINOR+1)); PATCH=0 ;;
      patch) PATCH=$((PATCH+1)) ;;
    esac
    NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"
    ;;
  *)
    # 显式版本号。校验形状：三段数字，可带预发布后缀
    NEW_VERSION="${BUMP#v}"
    printf '%s' "$NEW_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' \
      || die "版本号形状不合法：$NEW_VERSION（应形如 0.1.2 或 0.2.0-rc.1）"
    ;;
esac

NEW_TAG="v$NEW_VERSION"
# 预发布版不打 latest 镜像标签、不被自动更新拉到（release.yml 的约定）：
# rc 的语义是「候选」，把它推给所有实例等于拿生产环境做测试。
IS_PRE_RELEASE=0
case "$NEW_VERSION" in *-*) IS_PRE_RELEASE=1 ;; esac

if git ls-remote --exit-code --tags origin "refs/tags/$NEW_TAG" >/dev/null 2>&1; then
  die "tag $NEW_TAG 已存在于远端。换版本号，或先删：git push origin :refs/tags/$NEW_TAG"
fi
[ -n "$LATEST_TAG" ] && log "最新 tag：$LATEST_TAG"
log "本次发布：$NEW_TAG$([ "$IS_PRE_RELEASE" = "1" ] && echo '（预发布）')"

# ---------------------------------------------------------------
# 确认
# ---------------------------------------------------------------
if [ "$DRY_RUN" = "1" ]; then
  log "（dry-run）后续将执行：推送 $BRANCH → 打 tag $NEW_TAG 并推送 → 观察流水线 → 验证产物"
  log "（dry-run）结束，未改动任何状态"
  exit 0
fi

if [ "$ASSUME_YES" != "1" ]; then
  printf '即将发布 %s 到 %s。继续？[y/N] ' "$NEW_TAG" "$REPO"
  read -r ANSWER
  case "$ANSWER" in y|Y|yes|YES) ;; *) die "已取消" ;; esac
fi

# ---------------------------------------------------------------
# 4. 推送 main
# ---------------------------------------------------------------
step "推送 $BRANCH"

HEAD_SHA="$(git rev-parse HEAD)"
if [ "$HEAD_SHA" = "$(git rev-parse "origin/$BRANCH")" ]; then
  ok "本地与远端一致"
else
  git push origin "$BRANCH" || die "推送 $BRANCH 失败。解决推送问题后重新发布"
  git fetch origin "$BRANCH" --quiet
  [ "$(git rev-parse "origin/$BRANCH")" = "$HEAD_SHA" ] || die "推送后远端指针与本地不一致"
  ok "已推送 $BRANCH → ${HEAD_SHA:0:10}"
fi

# ---------------------------------------------------------------
# 5. 打 tag 并推送
# ---------------------------------------------------------------
step "打 tag $NEW_TAG"

git tag -a "$NEW_TAG" -m "$NEW_TAG" || die "打 tag 失败"
git push origin "$NEW_TAG" || {
  # 本地留着没推出去的 tag，会让下次发布算错版本号
  git tag -d "$NEW_TAG" >/dev/null 2>&1 || true
  die "推送 tag 失败（已删本地 tag）。解决后重新运行"
}
ok "tag $NEW_TAG 已推送，release.yml 应已触发"

# ---------------------------------------------------------------
# 6. 观察发布流水线
# ---------------------------------------------------------------
step "观察发布流水线"

# 推完 tag 到 Actions 注册有秒级延迟，轮询等它出现
RUN_ID=""
for _ in $(seq 1 12); do
  RUN_ID="$(gh run list --repo "$REPO" --workflow release.yml --limit 5 \
    --json databaseId,headBranch \
    --jq "[.[] | select(.headBranch == \"$NEW_TAG\")][0].databaseId" 2>/dev/null || true)"
  [ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] && break
  sleep 5
done
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] \
  || die "tag 已推但找不到 release.yml 的 run。检查：gh run list --repo $REPO --workflow release.yml（Actions 可能被禁用）"

log "run #$RUN_ID：https://github.com/$REPO/actions/runs/$RUN_ID"
# --exit-status：run 失败时命令返回非零；--interval 15 降低 API 用量
if ! gh run watch "$RUN_ID" --repo "$REPO" --exit-status --interval 15 2>&1 | tail -30; then
  warn "发布流水线失败。补救："
  warn "  看失败日志 : gh run view $RUN_ID --repo $REPO --log-failed"
  warn "  删除 tag   : git push origin :refs/tags/$NEW_TAG && git tag -d $NEW_TAG"
  warn "  删半成品   : gh release delete $NEW_TAG --repo $REPO --yes（若已创建）"
  exit 1
fi
ok "发布流水线全部通过"

# ---------------------------------------------------------------
# 7. 验证产物
# ---------------------------------------------------------------
step "验证产物"

# 流水线绿 ≠ 产物可用。归档名是 binary 通道拼下载 URL 的硬契约，
# 这里把「那个文件真的在 Release 上」验证掉。
ARCHIVE="llm-relay_${NEW_VERSION}_linux_amd64.tar.gz"
ASSETS="$(gh release view "$NEW_TAG" --repo "$REPO" --json assets --jq '[.assets[].name] | join("\n")')"

grep -Fx "$ARCHIVE" <<< "$ASSETS" >/dev/null \
  || die "Release 资产缺 $ARCHIVE（一键更新按这个名字拼 URL，缺了就是 404）。实际资产：$ASSETS"
grep -Fx "checksums.txt" <<< "$ASSETS" >/dev/null \
  || die "Release 资产缺 checksums.txt（更新时校验下载完整性用）"
ok "Release 资产齐全：$ARCHIVE + checksums.txt"

# 归档必须包含当前平台的产物。这是唯一能让「下载 → 校验 → 替换」
# 在服务器上走通的组合，缺一个平台就是那个平台的用户更新失败。
gh release download "$NEW_TAG" --repo "$REPO" --pattern "$ARCHIVE" --dir /tmp/llm-relay-release-verify --clobber \
  && tar -tzf "/tmp/llm-relay-release-verify/$ARCHIVE" | grep -Fx "llm-relay" \
  || warn "无法本地验证归档内容（可能本机不是 linux/amd64 或网络受限），跳过"
rm -rf /tmp/llm-relay-release-verify 2>/dev/null || true

# 镜像检查：docker 形态的更新来源。当前主形态是裸二进制，
# 所以失败只警告不拦发布。
if docker manifest inspect "$IMAGE_NAME:$NEW_VERSION" >/dev/null 2>&1; then
  ok "GHCR 镜像已发布：$IMAGE_NAME:$NEW_VERSION"
else
  warn "GHCR 镜像 $IMAGE_NAME:$NEW_VERSION 暂不可见（docker 通道受影响，binary 通道不受影响）"
fi

# ---------------------------------------------------------------
# 完成
# ---------------------------------------------------------------
RELEASE_URL="https://github.com/$REPO/releases/tag/$NEW_TAG"
echo
ok "v$NEW_VERSION 发布完成"
echo "  Release : $RELEASE_URL"
echo "  归档    : $ARCHIVE（binary 通道的一键更新来源）"
echo
echo "  服务器无需任何操作："
echo "    管理台 → 系统设置 → 版本更新 → 检测更新 → 一键更新"
echo "  （更新 = 下载归档 → sha256 校验 → 原子替换 → systemd 拉起，约 1-2 分钟）"
