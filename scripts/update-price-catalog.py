#!/usr/bin/env python3
"""更新内置的 LiteLLM 参考价库（backend/internal/pricing/catalog_data/litellm_prices.json）。

只负责「抓原文件、验可解析、落盘」三件事，**不做任何字段裁剪或换算**：
裁剪与换算统一在 Go 侧（pricing.ParseCatalogRaw）实现 —— 构建期（本脚本）
与运行期（管理台「更新参考价库」）走同一段裁剪逻辑，两边各写一份迟早漂移。

embed 的就是上游原文件，好处有二：
  - 可溯源：库里那份与上游 commit 逐字节一致，出问题能直接对 diff；
  - 解析失败时能明确归因到上游（而不是本脚本的转换 bug）。

用法（在仓库根目录）：
    python3 scripts/update-price-catalog.py
    python3 scripts/update-price-catalog.py --proxy-prefix https://gh-proxy.com
    python3 scripts/update-price-catalog.py --check   # 只校验现有文件，不下载

产物不入 .gitignore，抓完需要提交（前端 dist 同款约定：数据随二进制走）。
"""

import argparse
import hashlib
import json
import pathlib
import sys
import urllib.request

DEFAULT_URL = (
    "https://raw.githubusercontent.com/BerriAI/litellm/main/"
    "model_prices_and_context_window.json"
)

# 上游每隔几天就有一次调价提交，抓不下来时宁可留着旧文件并明确报错，
# 也不要写半截 JSON 进仓库（embed 是编译期行为，坏文件会让 go build 直接失败）。
MAX_BYTES = 32 * 1024 * 1024


def fetch(url: str) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "llm-relay-catalog-sync"})
    with urllib.request.urlopen(req, timeout=120) as resp:
        data = resp.read(MAX_BYTES + 1)
    if len(data) > MAX_BYTES:
        raise SystemExit(f"文件超过 {MAX_BYTES} 字节上限，疑似抓错目标")
    return data


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--url", default=DEFAULT_URL, help="上游价格表地址")
    ap.add_argument(
        "--proxy-prefix",
        default="",
        help="GitHub 加速前缀（如 https://gh-proxy.com），拼在 --url 前面",
    )
    ap.add_argument(
        "--out",
        default=str(
            pathlib.Path(__file__).resolve().parent.parent
            / "backend/internal/pricing/catalog_data/litellm_prices.json"
        ),
        help="输出路径",
    )
    ap.add_argument("--check", action="store_true", help="只校验现有文件，不下载")
    args = ap.parse_args()

    out = pathlib.Path(args.out)
    if args.check:
        if not out.exists():
            print(f"缺少数据文件: {out}")
            return 1
        data = out.read_bytes()
    else:
        url = args.proxy_prefix + args.url if args.proxy_prefix else args.url
        print(f"下载 {url} ...")
        data = fetch(url)

    try:
        parsed = json.loads(data.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as e:
        print(f"文件不是合法 JSON: {e}")
        return 1
    if not isinstance(parsed, dict) or len(parsed) < 100:
        print(f"内容不像价格表（{len(parsed) if isinstance(parsed, dict) else type(parsed).__name__} 条）")
        return 1

    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_bytes(data)
    print(f"已写入 {out}")
    print(f"条目数: {len(parsed)}")
    print(f"大小: {len(data)} 字节")
    print(f"sha256: {hashlib.sha256(data).hexdigest()}")
    print("记得提交这个文件 —— embed 是编译期行为，不提交则 CI 构建缺数据。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
