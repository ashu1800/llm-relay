#!/bin/sh
# ============================================================
#  resolve-version.sh —— 为构建解析出**一个**版本号
#
#  谁在用：
#    · Makefile 的 VERSION 变量（make build / make build-embed）
#    · deploy/Dockerfile 的 backend-build 阶段（VERSION 构建参数为空时兜底）
#
#  解析优先级（高 → 低），只有两级 —— 来源越多，「界面上的版本号」与
#  「真正在跑的代码」就越容易对不上：
#    1. 当前提交上**精确匹配**的 git tag（v0.1.2 或 0.1.2），去掉 v 前缀
#    2. backend/internal/version/VERSION 文件内容（去掉 CR/LF）
#
#  为什么是这个顺序：
#    · 打了 tag 就代表「这一版被正式命名过」，tag 是唯一权威。
#      用 --exact-match 而不是 describe 默认的「最近一个 tag」：后者会把
#      v0.1.0 之后 30 个未发布提交也报成 0.1.0，产出的归档会张冠李戴 ——
#      发布产物必须与它声称的版本严格对应。
#    · 没有 tag（日常开发构建）时回落到 VERSION 文件，这样「本地随手 build」
#      与「CI 发布」在同一个提交上得到同一个版本号。
#
#  输出保证：单行、**不带 v 前缀**、不带 CR。这与发布产物的命名契约一致：
#  归档名 llm-relay_<version>_<goos>_<goarch>.tar.gz 里的 <version> 不带 v。
#  后端拼下载 URL 时用的就是这个值，多一个 v 就会 404。
# ============================================================
set -eu

# CDPATH= 是为了防调用方环境里的 CDPATH 让 cd 顺带打印一行路径 ——
# 那样 $(...) 捕获到的就是「路径+路径」，后面的拼接会全部错位。
# -- 是 POSIX 的选项终止符：目录名以 - 开头时也不会被当成 cd 的选项。
# 用 $0 而不是 $BASH_SOURCE：本脚本按 POSIX sh 写，Alpine 里由 busybox sh 执行。
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# 本脚本位于 backend/scripts/，所以脚本目录的上一级就是 backend/
BACKEND_DIR="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)"
# 仓库根：backend/ 的上一级。git 命令用 -C 指向它，而不是先 cd 过去 ——
# 调用方的当前目录不该被这个脚本改掉。
REPO_DIR="$(CDPATH= cd -- "$BACKEND_DIR/.." && pwd)"
# VERSION 文件必须待在 version 包目录里，不能挪到仓库根：
# version.go 用 //go:embed VERSION 读它，而 embed 只能引用同包目录下的文件。
VERSION_FILE="$BACKEND_DIR/internal/version/VERSION"

# ---- 第一优先级：精确匹配的 git tag ----
# command -v 先探一次：没装 git（例如在精简容器里构建）时直接走第二优先级，
# 而不是让 git 报 command not found 把 set -e 打炸。
if command -v git >/dev/null 2>&1; then
  # 两种 tag 形状都认：v0.1.2（本仓库约定）与 0.1.2（有人手工打的那种）。
  # 2>/dev/null 吞掉「当前提交没有 tag」时 git 打到 stderr 的提示；
  # 末尾的 || true 兜住 set -e —— 没有 tag 是完全正常的路径，不是错误。
  TAG="$(git -C "$REPO_DIR" describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || \
         git -C "$REPO_DIR" describe --tags --exact-match --match '[0-9]*' 2>/dev/null || true)"
  if [ -n "$TAG" ]; then
    # ${TAG#v} 只剥掉开头那一个 v：v0.1.2 -> 0.1.2，0.1.2 -> 0.1.2（不变）。
    # 幂等：两种形状都得到同一个结果，不用再判断该不该剥。
    printf '%s\n' "${TAG#v}"
    exit 0
  fi
fi

# ---- 第二优先级：VERSION 文件内容 ----
# tr -d '\r\n' 同时去掉 CR 与 LF：这个文件在 Windows 上（core.autocrlf=true）
# 可能被检出成 CRLF，带着 \r 的版本号经 -ldflags 注入后会变成 "0.1.0\r" ——
# 界面上完全看不出区别，但 semver 解析与下载 URL 拼接都会出错。
# 用重定向 < 而不是把文件名当参数：tr 读的是文件内容，不需要额外处理文件名。
printf '%s\n' "$(tr -d '\r\n' < "$VERSION_FILE")"
