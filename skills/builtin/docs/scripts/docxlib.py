"""Word 读取适配层：由镜像预装的 python-docx 负责解析。

只统一标题、合并单元格和输出块的读法，不保存或修改源文件。
ZIP 预检保留正文 XML 的大小边界；正文按 Document.iter_inner_content 保持原始顺序。
"""

import zipfile

MAX_CHARS = 2_000_000
MAX_XML_BYTES = 80 << 20


class DocxError(Exception):
    """带用户可读原因的读取失败。"""


def _detail(error):
    """损坏包可能提供任意长度的错误文字，CLI 只输出有界原因。"""
    return " ".join(str(error).split())[:240]


def _check_package(path):
    try:
        with zipfile.ZipFile(path) as package:
            if "word/document.xml" not in package.namelist():
                raise DocxError("docx 缺少 word/document.xml（可能不是 Word 文档）")
            if package.getinfo("word/document.xml").file_size > MAX_XML_BYTES:
                raise DocxError("document.xml 超过 80MB，拒绝解析（可能是解压炸弹）")
    except (zipfile.BadZipFile, OSError) as error:
        raise DocxError(f"不是可读的 docx（zip 层损坏）: {_detail(error)}") from error


def open_body(path):
    """加载只读 Document；函数名保留供两个 CLI 共用。"""
    try:
        from docx import Document
    except ImportError as error:
        raise DocxError(
            "Word 读取运行库 python-docx 未就绪，请使用预装依赖的 Pogo 镜像"
            "及 /opt/venv/bin/python3；无需在公共环境临时安装。"
        ) from error
    _check_package(path)
    try:
        document = Document(path)
        if not callable(getattr(document, "iter_inner_content", None)):
            raise DocxError("python-docx 版本过旧，请更新包含 python-docx 1.2 的 Pogo 镜像")
        return document
    except DocxError:
        raise
    except Exception as error:
        raise DocxError(f"docx 文档结构不可读取: {_detail(error)}") from error


def para_text(paragraph):
    """python-docx 1.2 包含超链接文本，并将 tab/br/cr 映射为空白。"""
    return paragraph.text


def _style_heading(value):
    value = (value or "").lower()
    for needle in ("heading", "标题"):
        index = value.find(needle)
        if index < 0:
            continue
        tail = value[index + len(needle):].strip()
        if tail.isdigit() and 1 <= int(tail) <= 9:
            return int(tail)
        if not tail:
            return 2  # 保留已有工具对无编号 Heading/标题 样式的读法。
    return 0


def _outline_heading(properties):
    if properties is None or properties.outlineLvl is None:
        return 0
    value = properties.outlineLvl.val
    return value + 1 if value is not None and 0 <= value <= 8 else 0


def para_heading(paragraph):
    """识别中英文样式名/ID；自定义标题兼容直接或继承的大纲级别。"""
    level = _style_heading(paragraph._p.style)
    if level:
        return level
    style = paragraph.style
    if style is not None:
        for value in (style.style_id, style.name):
            level = _style_heading(value)
            if level:
                return level
    properties = paragraph._p.pPr
    if properties is not None and properties.outlineLvl is not None:
        return _outline_heading(properties)
    seen = set()
    while style is not None and style.style_id not in seen:
        seen.add(style.style_id)
        properties = style.element.pPr
        if properties is not None and properties.outlineLvl is not None:
            return _outline_heading(properties)
        style = style.base_style
    return 0


def cell_text(cell):
    """单元格内多段落合并成一行，保持既有 Markdown 输出契约。"""
    parts = [para_text(paragraph).strip() for paragraph in cell.paragraphs]
    return " ".join(part for part in parts if part)


def table_rows(table):
    """按网格列输出：合并值只在左上格出现一次，其余格为空。"""
    rows = []
    seen_cells = set()
    for row in table.rows:
        cells = [""] * row.grid_cols_before
        for cell in row.cells:
            # python-docx 会把水平/垂直合并区映射到同一个原始单元格。
            # 保留元素引用而非 id，避免迭代中对象回收导致误判。
            if cell._tc in seen_cells:
                cells.append("")
            else:
                seen_cells.add(cell._tc)
                cells.append(cell_text(cell))
        cells.extend([""] * row.grid_cols_after)
        rows.append(cells)
    return rows


def body_items(document):
    """按段落/表格的交错顺序产出块，不分开遍历 paragraphs/tables。"""
    from docx.table import Table
    from docx.text.paragraph import Paragraph

    for item in document.iter_inner_content():
        if isinstance(item, Paragraph):
            yield "para", item
        elif isinstance(item, Table):
            yield "table", item
