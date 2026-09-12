#!/usr/bin/env bash
# 重新部署并验证。
#
# 这里必须把构建失败当成硬错误。踩过的坑：install.sh 构建失败时**旧容器还在跑**，
# healthz 照样返回 ok，脚本于是「成功」返回，后续验证测的其实是旧二进制。
# 当时 limit.go 里多了一个同名的 Inflight 方法导致编译不过，
# 并发验证跑在未修复的旧容器上，得出了「修复无效」的错误结论，
# 白花了一轮排查。
set -uo pipefail
cd "/path/to/llm-relay" || exit 1

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
