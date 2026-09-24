#!/usr/bin/env bash
# 管理接口登录鉴权的脚本侧适配（供其它 test-*.sh 脚本 source）。
#
# 背景：管理后台上线密钥登录后，直接 curl /api/admin/* 会得到 401。
# 逐个脚本改写 curl 调用不现实（几十处），所以这里的做法是：
# 定义一个与命令同名的 curl 包装函数 —— 首次被调用时用 RELAY_ADMIN_KEY
# 登录一次拿会话 Cookie，之后给每次 curl 注入 `-b <CookieJar>`。
# 调用方脚本除了一行 source + 一行 setup 之外零改动。
#
# 用法（在脚本里、任何 curl 之前）：
#   source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup
#
# 行为约定：
#   - 未设置 RELAY_ADMIN_KEY（环境变量、./deploy/.env、/opt/llm-relay/deploy/.env
#     三处都找不到）时视为「鉴权关闭」，不装包装函数，行为与从前完全一致；
#   - 登录失败不把脚本打死：网络类失败（服务没起、正被 test-coldstart.sh
#     拆建中）会在下一次 curl 时自动重试登录；4xx 类失败（密钥错、鉴权关闭）
#   - 服务端地址默认 http://127.0.0.1:8888，可用环境变量 ADMIN_BASE 覆盖，
#     或跟随脚本自己的 API 变量（剥掉 /api/admin 尾巴）推导；
#   - 会话是服务端签发的无状态 HMAC 令牌，服务重启不影响已登录的 Cookie。
admin_auth_setup() {
  # 幂等：重复 source + setup 只装一次
  if [ -n "${_ADMIN_AUTH_DONE:-}" ]; then return 0; fi
  _ADMIN_AUTH_DONE=1

  local key=""
  if [ -n "${RELAY_ADMIN_KEY:-}" ]; then
    key="$RELAY_ADMIN_KEY"
  else
    local envfile
    for envfile in ./deploy/.env /opt/llm-relay/deploy/.env; do
      if [ -f "$envfile" ]; then
        key="$(grep '^RELAY_ADMIN_KEY=' "$envfile" | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]")"
        [ -n "$key" ] && break
      fi
    done
  fi
  # 没配密钥 = 鉴权关闭，无需任何适配
  if [ -z "$key" ]; then return 0; fi
  ADMIN_KEY_VALUE="$key"

  ADMIN_COOKIE_JAR="$(mktemp)"
  export ADMIN_COOKIE_JAR

  # 与 curl 命令同名的包装函数：`$( )` 子 shell 与管道里同样生效。
  # 登录放在首次调用而不是 setup 时：部分脚本先 source、后定义 API/BASE，
  # 提前登录会打错地址；惰性触发时脚本自己的变量已就绪。
  #
  # 「已登录」标记必须落在**文件**上而不是 shell 变量：脚本里大量
  # `SK=$(curl ...)` 式调用把执行放进子 shell，子 shell 里改的变量
  # 活不到下一次调用 —— 每次都会重登录一次（还会吃掉登录限流额度）。
  # CookieJar 同级的 .done 文件没有这个问题。
  curl() {
    if [ ! -f "${ADMIN_COOKIE_JAR}.done" ]; then
      local base="${ADMIN_BASE:-}"
      if [ -z "$base" ] && [ -n "${API:-}" ]; then base="${API%/api/admin}"; fi
      base="${base:-http://127.0.0.1:8888}"
      local rc=0
      # -f：HTTP 4xx/5xx 也算失败。区分两类失败：
      #   4xx（rc=22，密钥错/鉴权关闭）→ 记为已尝试，不再重复登录；
      #   网络失败（服务没起/正被拆建）→ 不做标记，下次 curl 再试一次。
      command curl -sf -m 10 -c "$ADMIN_COOKIE_JAR" -X POST "$base/api/admin/auth/login" \
        -H 'Content-Type: application/json' -d "{\"key\":\"$ADMIN_KEY_VALUE\"}" >/dev/null || rc=$?
      if [ "$rc" -eq 0 ] || [ "$rc" -eq 22 ]; then
        : > "${ADMIN_COOKIE_JAR}.done"
      fi
    fi
    command curl -b "$ADMIN_COOKIE_JAR" "$@"
  }
}
