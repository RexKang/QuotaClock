#!/usr/bin/env python3
"""拼装 GitHub Release 正文（发布流程用，本地可预览）。

正文 = 固定说明 + CHANGELOG.md 里该版本的小节 + 两版之间的提交明细。

用法：
    python scripts/release_notes.py v0.2.5                 # 打印到 stdout（本地预览）
    python scripts/release_notes.py v0.2.5 -o notes.md     # 写文件（CI 里给 goreleaser 用）
    python scripts/release_notes.py v0.2.5 --prev v0.2.4   # 指定上一版本标签（默认自动取）

为什么不用 goreleaser 自带的 changelog：它的输出是「40 位 SHA + 提交标题」的流水账，
用户看到的 Release 页面里那段说明每次都一样、明细又读不出重点。本脚本把手写的
CHANGELOG 小节放在最前（本版重点），提交明细放后面（可追溯），中文标题分组。
"""
import argparse
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CHANGELOG = ROOT / "CHANGELOG.md"

# 提交类型 → 中文分组（未列出的类型进「其他」；docs/chore/test 不列进发布说明）
GROUPS = [
    ("新增", ("feat",)),
    ("修复", ("fix",)),
    ("性能", ("perf",)),
    ("重构", ("refactor",)),
    ("其他", ("build", "ci", "style", "revert")),
]
HIDDEN = ("docs", "chore", "test")  # 不写进 Release 正文（与 .goreleaser 原过滤一致）

HEADER = """## QuotaClock {tag}

单二进制服务端，下载对应平台产物解压即用。
首次启动生成模板配置；默认密码 `Quota@2026090S`，登录后请立即修改。
校验和见 `checksums.txt`（sha256）。
"""


def run(*args):
    return subprocess.run(args, cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()


def try_run(*args):
    """执行 git 命令，失败返回 None（标签不存在、无父提交等场景都要能预览）。"""
    try:
        return run(*args)
    except subprocess.CalledProcessError:
        return None


def norm(tag):
    """v0.2.5 / 0.2.5 都归一到 0.2.5，用于匹配 CHANGELOG 小节。"""
    return tag.lstrip("vV")


def rev_of(tag):
    """标签指向的提交；标签还没打（本地预览）时退回 HEAD。"""
    return tag if try_run("git", "rev-parse", "--verify", f"{tag}^{{commit}}") else "HEAD"


def prev_tag(tag):
    """上一版本标签：标签存在则取它的父提交上最近的标签；否则取 HEAD 上最近的标签。"""
    if try_run("git", "rev-parse", "--verify", f"{tag}^{{commit}}"):
        return try_run("git", "describe", "--tags", "--abbrev=0", f"{tag}^")
    return try_run("git", "describe", "--tags", "--abbrev=0", "HEAD")


def changelog_section(tag):
    """取出 CHANGELOG.md 中该版本的小节正文（不含标题行）。取不到返回 None。"""
    if not CHANGELOG.exists():
        return None
    lines = CHANGELOG.read_text(encoding="utf-8").splitlines()
    want = norm(tag)
    start = None
    for i, line in enumerate(lines):
        m = re.match(r"^##\s+\[?v?([0-9][^\]]*?)\]?\s*(?:-\s*(.*))?$", line.strip())
        if m and m.group(1).split()[0] == want:
            start = i + 1
            break
    if start is None:
        return None
    end = len(lines)
    for j in range(start, len(lines)):
        if lines[j].startswith("## "):
            end = j
            break
    return "\n".join(lines[start:end]).strip()


def commits(prev, tag):
    """两版之间的提交标题（标签未打时用 HEAD 作为终点，便于本地预览）。"""
    to = rev_of(tag)
    rng = f"{prev}..{to}" if prev else to
    out = try_run("git", "log", "--no-merges", "--pretty=%s", rng)
    return [line for line in (out or "").splitlines() if line.strip()]


def split_subject(subject):
    """'feat(ui): xxx' → ('feat', 'ui', 'xxx')；无前缀 → ('', '', subject)。"""
    m = re.match(r"^([a-zA-Z]+)(?:\(([^)]*)\))?!?:\s*(.+)$", subject)
    if not m:
        return "", "", subject
    return m.group(1).lower(), m.group(2) or "", m.group(3).strip()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("tag", help="版本标签，如 v0.2.5")
    ap.add_argument("-o", "--out", help="输出文件（缺省打印到 stdout）")
    ap.add_argument("--prev", help="上一版本标签（缺省自动取该标签的前一个）")
    ap.add_argument("--no-commits", action="store_true", help="不附提交明细")
    ap.add_argument("--strict", action="store_true",
                    help="CHANGELOG.md 缺该版本小节时直接失败（CI 用：逼着发布前先写更新日志）")
    args = ap.parse_args()

    tag = args.tag
    parts = [HEADER.format(tag=tag).rstrip()]

    section = changelog_section(tag)
    if section:
        parts.append("## 本版重点\n\n" + section)
    elif args.strict:
        sys.exit(f"CHANGELOG.md 里没有 {tag} 的小节：发布前请先在 CHANGELOG.md 写清本版新增/变更/修复"
                 f"（小节标题形如 `## [{tag}] - YYYY-MM-DD`）")
    else:
        print(f"[warn] CHANGELOG.md 里没有 {tag} 的小节（正文只含提交明细）", file=sys.stderr)

    if not args.no_commits:
        prev = args.prev or prev_tag(tag)
        items = commits(prev, tag)
        buckets = {name: [] for name, _ in GROUPS}
        for subj in items:
            typ, scope, text = split_subject(subj)
            if typ in HIDDEN:
                continue
            for name, types in GROUPS:
                if typ in types:
                    buckets[name].append(f"- {text}" + (f"（{scope}）" if scope else ""))
                    break
            else:
                buckets["其他"].append(f"- {text}")
        body = [f"### 提交明细（{prev or '初始版本'} → {tag}）"]
        for name, _ in GROUPS:
            if buckets[name]:
                body.append(f"\n**{name}**")
                body.extend(buckets[name])
        if len(body) > 1:
            parts.append("\n".join(body))

    notes = "\n\n".join(parts).rstrip() + "\n"
    if args.out:
        Path(args.out).write_text(notes, encoding="utf-8")
        print(f"已写入 {args.out}（{len(notes)} 字符）")
    else:
        sys.stdout.write(notes)


if __name__ == "__main__":
    main()
