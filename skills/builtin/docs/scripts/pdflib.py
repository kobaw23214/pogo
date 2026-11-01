#!/opt/venv/bin/python3
"""pdf 技能的公共防护与文档打开逻辑。

错误约定（clig 风格）：数据走 stdout，单行 `ERROR:<code>: <msg>` 走 stderr。
退出码：0 成功；2 用法错；3 文件超限（大小/页数）；4 无文字层；5 加密；
6 打不开/损坏。代码给程序，msg 给模型。
"""
import os
import re
import sys

import pymupdf

MAX_BYTES = 300 * 1024 * 1024   # 文件大小上限：APT 报告远小于此，挡的是武器化畸形文件
MAX_PAGES = 3000
PAGE_NO_RE = re.compile(r"^\s*(?:page\s*)?\d+(?:\s*(?:of|/)\s*\d+)?\s*$", re.I)
STRIP_FRAC = 0.08               # 页顶/页底条带高度（页高的 8%）→ 页眉页脚检测区
PAGE_CHAR_BUDGET = 6000         # 单页输出预算；超出截断并标注，模型可切小页段重取


class Fail(Exception):
    def __init__(self, code, msg):
        super().__init__(msg)
        self.code = code
        self.msg = msg


def open_guarded(path):
    """带防护地打开：存在性/大小/加密/页数，返回 (doc, repaired)。"""
    if not path or not os.path.isfile(path):
        raise Fail(2, f"文件不存在: {path!r}")
    if os.path.getsize(path) > MAX_BYTES:
        raise Fail(3, f"文件超过 {MAX_BYTES // (1024 * 1024)}MB 上限，拒绝解析")
    try:
        doc = pymupdf.open(path)  # 自动尝试修复损坏结构
    except Exception as e:  # noqa: BLE001 — 任何打开失败都归一为退出码 6
        raise Fail(6, f"无法打开（损坏或非 PDF）: {e}")
    if doc.needs_pass:
        raise Fail(5, "加密 PDF 需要密码，本技能不支持")
    if doc.page_count > MAX_PAGES:
        raise Fail(3, f"页数 {doc.page_count} 超过上限 {MAX_PAGES}")
    return doc


def normalize_header(text):
    """页眉/页脚的归一化键：去掉数字与空白，只留下可重复的骨架。"""
    return re.sub(r"[\d\s]+", "", text or "").strip().lower()


def page_blocks(page):
    """返回文本块 [(x0,y0,x1,y1,text)]，图像块丢弃。"""
    out = []
    for b in page.get_text("blocks"):
        if b[6] != 0:
            continue
        t = (b[4] or "").strip()
        if t:
            out.append((b[0], b[1], b[2], b[3], t))
    return out


def strip_norm(text):
    return normalize_header(text)


def run(main):
    try:
        main()
    except Fail as f:
        print(f"ERROR:{f.code}: {f.msg}", file=sys.stderr)
        sys.exit(f.code)
    except BrokenPipeError:
        sys.exit(0)


if __name__ == "__main__":
    pass
