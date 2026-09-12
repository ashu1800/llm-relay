#!/usr/bin/env bash
cd "/path/to/llm-relay" || exit 1
fail=0
for f in deploy/*.sh scripts/*.sh; do
  if ! bash -n "$f" 2>/dev/null; then
    echo "  语法错误: $f"
    bash -n "$f"
    fail=$((fail+1))
  fi
done
echo "  检查了 $(ls deploy/*.sh scripts/*.sh | wc -l) 个脚本，语法错误 $fail 个"
exit $fail
