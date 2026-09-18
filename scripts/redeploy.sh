#!/usr/bin/env bash
# 重新部署并验证。
#
# 这里必须把构建失败当成硬错误。踩过的坑：install.sh 构建失败时**旧容器还在跑**，
# healthz 照样返回 ok，脚本于是「成功」返回，后续验证测的其实是旧二进制。
# 当时 limit.go 里多了一个同名的 Inflight 方法导致编译不过，
# 并发验证跑在未修复的旧容器上，得出了「修复无效」的错误结论，
# 白花了一轮排查。
#
# 前端产物也要在这里构建并拷进 backend/internal/web/dist：
# Go 用 go:embed all:dist 把前端打进二进制，而 install.sh 明确排除了
# ./backend/internal/web/dist（那是构建产物，不该从仓库带过去）。
# 所以那一步拷贝以前全靠手工 —— 忘了做就是「后端更新了、页面还是旧的」，
# 而且不会有任何报错。
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

# ---- 前端 ----
# 只做类型检查，不在这里构建产物。
# 真正的前端产物由 deploy/Dockerfile 的 Stage 1 构建并 COPY 进嵌入目录，
# 本地再怎么构建都会被它覆盖 —— 这里构建纯属重复劳动。
# 但类型检查要留着：它和下面的 go build 一样，能在本地就把错误拦下来，
# 不用等 docker build 跑完再去翻日志。
if ! (cd frontend && npm run type-check > /tmp/web-typecheck.log 2>&1); then
  echo "前端类型检查未通过，已中止部署（容器仍运行旧版本）："
  tail -25 /tmp/web-typecheck.log
  exit 1
fi
echo "前端类型检查通过"

# 契约检查也放在这里。它盯的是一批「不报错、不失败、只是悄悄不对」的东西：
# 双份定义的色值漂移、被删掉还在被引用的令牌、光标热区与图形对不上之类。
# 以前只在手工跑 npm run check 时才看得到，改坏了没人拦 ——
# 而这类问题恰恰是编译器与类型系统看不见的那一半。
if ! (cd frontend && npm run check > /tmp/web-contracts.log 2>&1); then
  echo "前端契约检查未通过，已中止部署（容器仍运行旧版本）："
  grep -E 'FAIL|未通过' /tmp/web-contracts.log | head -25
  exit 1
fi
echo "前端契约检查通过"

# ---- 后端 ----
# 先本地编译一遍：语法与类型错误在这里能直接看到编译器原文，
# 不用等 docker build 跑完再去 tail 日志
if ! (cd backend && GOPROXY=https://goproxy.cn,direct GOSUMDB=off go build ./... 2>&1); then
  echo "本地编译失败，已中止部署（容器仍运行旧版本）"
  exit 1
fi

if ! bash deploy/install.sh > /tmp/deploy-font.log 2>&1; then
  echo "install.sh 失败，最后 25 行日志："
  tail -25 /tmp/deploy-font.log
  exit 1
fi

for i in $(seq 1 45); do
  if curl -fsS -m 3 http://127.0.0.1:8888/healthz >/dev/null 2>&1; then break; fi
  sleep 2
done

# 镜像必须比所有源码新，否则跑的还是旧二进制
newest=$(find backend -name '*.go' -newer /tmp/deploy-font.log -print -quit 2>/dev/null || true)
if [ -n "$newest" ]; then
  echo "警告: $newest 比本次构建还新，重新构建"
  if ! bash deploy/install.sh > /tmp/deploy-font.log 2>&1; then
    echo "重新构建失败，最后 25 行日志："
    tail -25 /tmp/deploy-font.log
    exit 1
  fi
fi

echo "install.sh OK"
echo "健康: $(curl -s -m 5 http://127.0.0.1:8888/healthz)"
echo "容器: $(docker ps --filter name=llm-relay --format '{{.Names}} {{.Status}}' | tr '\n' ' ')"
