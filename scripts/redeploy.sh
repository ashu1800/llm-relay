#!/usr/bin/env bash
cd "/path/to/llm-relay" || exit 1
bash deploy/install.sh > /tmp/deploy-font.log 2>&1
echo "install.sh EXIT=$?"
for i in $(seq 1 45); do
  if curl -fsS -m 3 http://127.0.0.1:8888/healthz >/dev/null 2>&1; then break; fi
  sleep 2
done
echo "健康: $(curl -s -m 5 http://127.0.0.1:8888/healthz)"
echo "容器: $(docker ps --filter name=llm-relay --format '{{.Names}} {{.Status}}' | tr '\n' ' ')"
