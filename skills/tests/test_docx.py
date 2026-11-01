"""CLI contract checks against synthetic documents; requires python-docx >= 1.2.

Run: python3 -m unittest discover -s pogo/skills/tests -v
DOCX_SCRIPTS may point at a saved old reader to inspect compatibility differences.
No actual user documents or Office services are used.
"""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile

from docx import Document
from docx.enum.style import WD_STYLE_TYPE
from docx.oxml import OxmlElement
from docx.oxml.ns import qn
from docx.opc.constants import RELATIONSHIP_TYPE


SCRIPTS = Path(os.environ.get("DOCX_SCRIPTS", Path(__file__).resolve().parents[1] / "builtin" / "docs" / "scripts"))


def hyperlink(paragraph, label):
    link = OxmlElement("w:hyperlink")
    link.set(qn("r:id"), paragraph.part.relate_to("https://example.invalid/", RELATIONSHIP_TYPE.HYPERLINK, is_external=True))
    run = OxmlElement("w:r")
    text = OxmlElement("w:t")
    text.text = label
    run.append(text)
    link.append(run)
    paragraph._p.append(link)


class DocxCliTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="pogo-docx-contract-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.path = self.root / "synthetic.docx"

    def cli(self, name, *args):
        return subprocess.run([sys.executable, str(SCRIPTS / name), *map(str, args)], capture_output=True, text=True, timeout=15)

    def ok(self, result):
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        return result.stdout.rstrip("\n")

    def mixed_document(self):
        document = Document()
        document.add_heading("一、总览", level=1)
        document.add_paragraph("正文甲")
        table = document.add_table(rows=3, cols=3)
        table.cell(0, 0).merge(table.cell(0, 1)).text = "横向"
        table.cell(0, 2).text = "右侧"
        table.cell(1, 0).merge(table.cell(2, 0)).text = "纵向"
        table.cell(1, 1).text = "乙"
        table.cell(1, 2).text = "丙"
        table.cell(2, 1).text = "丁"
        table.cell(2, 2).text = "戊"
        style = document.styles.add_style("标题 3", WD_STYLE_TYPE.PARAGRAPH)
        style._element.set(qn("w:styleId"), "LocalizedCustom3")
        document.add_paragraph("中文样式标题", style=style)
        paragraph = document.add_paragraph("大纲标题")
        outline = OxmlElement("w:outlineLvl")
        outline.set(qn("w:val"), "3")
        paragraph._p.get_or_add_pPr().append(outline)
        paragraph = document.add_paragraph("链接前")
        hyperlink(paragraph, "超链接")
        paragraph.add_run("\t尾\n下一行")
        document.add_paragraph("")
        document.add_paragraph("结尾")
        document.save(self.path)

    def test_info_compact_json_heading_navigation_and_counts(self):
        self.mixed_document()
        raw = self.ok(self.cli("docx-info", self.path))
        self.assertEqual(len(raw.splitlines()), 1)
        info = json.loads(raw)
        self.assertEqual(set(info), {"paragraphs", "tables", "chars", "headings"})
        self.assertEqual(info["paragraphs"], 7)
        self.assertEqual(info["tables"], 1)
        self.assertEqual(info["chars"], sum(map(len, ["一、总览", "正文甲", "中文样式标题", "大纲标题", "链接前超链接\t尾\n下一行", "", "结尾"])))
        self.assertEqual(info["headings"], [{"lvl": 1, "text": "一、总览"}, {"lvl": 3, "text": "中文样式标题"}, {"lvl": 4, "text": "大纲标题"}])

    def test_markdown_preserves_interleaved_blocks_links_and_whitespace(self):
        self.mixed_document()
        output = self.ok(self.cli("docx-text", self.path))
        markers = [line for line in output.splitlines() if line.startswith("<!--")]
        self.assertEqual(markers, [f"<!--{number}-->" for number in [1, 2, 3, 4, 5, 6, 8]])
        self.assertIn("# 一、总览", output)
        self.assertIn("### 中文样式标题", output)
        self.assertIn("#### 大纲标题", output)
        self.assertIn("链接前超链接\t尾\n下一行", output)
        self.assertLess(output.index("正文甲"), output.index("横向"))
        self.assertLess(output.index("横向"), output.index("中文样式标题"))

    def test_block_range_and_empty_blocks_have_stable_addressing(self):
        self.mixed_document()
        output = self.ok(self.cli("docx-text", self.path, 3, 4))
        self.assertIn("<!--3-->", output)
        self.assertIn("<!--4-->", output)
        self.assertIn("横向", output)
        self.assertIn("中文样式标题", output)
        self.assertNotIn("正文甲", output)
        self.assertNotIn("大纲标题", output)
        self.assertEqual(self.ok(self.cli("docx-text", self.path, 7, 7)), "(空范围)")
        self.assertEqual(self.ok(self.cli("docx-text", self.path, 99, 100)), "(空范围)")

    def test_merged_cells_keep_value_once_and_other_columns_aligned(self):
        self.mixed_document()
        output = self.ok(self.cli("docx-text", self.path, 3, 3))
        self.assertEqual(output.count("横向"), 1)
        self.assertEqual(output.count("纵向"), 1)
        self.assertIn("| 横向 |  | 右侧 |", output)
        self.assertIn("| 纵向 | 乙 | 丙 |", output)
        self.assertIn("|  | 丁 | 戊 |", output)

    def test_table_escapes_pipes_and_flattens_cell_paragraphs(self):
        document = Document()
        table = document.add_table(rows=2, cols=2)
        table.cell(0, 0).text = "列|一"
        table.cell(0, 1).text = "列二"
        table.cell(1, 0).text = "第一行\n第二行"
        table.cell(1, 0).add_paragraph("第三行")
        table.cell(1, 1).text = "数值"
        document.save(self.path)
        output = self.ok(self.cli("docx-text", self.path))
        self.assertIn("| 列\\|一 | 列二 |", output)
        self.assertIn("| 第一行 第二行 第三行 | 数值 |", output)

    def test_output_budget_has_explicit_reason_and_resume_block(self):
        document = Document()
        document.add_paragraph("前言")
        document.add_paragraph("超" * 120_100)
        document.save(self.path)
        output = self.ok(self.cli("docx-text", self.path))
        self.assertIn("<!--1-->", output)
        self.assertIn("前言", output)
        self.assertIn("已截断", output)
        self.assertIn("字符预算", output)
        self.assertIn("第 2 块", output)
        self.assertLess(len(output), 120_500)

    def test_navigation_budget_is_bounded(self):
        document = Document()
        for index in range(205):
            document.add_heading(f"{index}:" + "章" * 140, level=1)
        document.save(self.path)
        info = json.loads(self.ok(self.cli("docx-info", self.path)))
        self.assertEqual(info["paragraphs"], 205)
        self.assertEqual(len(info["headings"]), 200)
        self.assertTrue(all(len(heading["text"]) <= 120 for heading in info["headings"]))

    def test_heading_outline_inherited_from_custom_style(self):
        document = Document()
        base = document.styles.add_style("报告章节", WD_STYLE_TYPE.PARAGRAPH)
        outline = OxmlElement("w:outlineLvl")
        outline.set(qn("w:val"), "1")
        base._element.get_or_add_pPr().append(outline)
        child = document.styles.add_style("报告子章节样式", WD_STYLE_TYPE.PARAGRAPH)
        child.base_style = base
        document.add_paragraph("通过样式继承的大纲章节", style=child)
        document.save(self.path)
        info = json.loads(self.ok(self.cli("docx-info", self.path)))
        self.assertEqual(info["headings"], [{"lvl": 2, "text": "通过样式继承的大纲章节"}])
        self.assertIn("## 通过样式继承的大纲章节", self.ok(self.cli("docx-text", self.path)))

    def test_imported_heading_style_reference_keeps_existing_navigation(self):
        document = Document()
        paragraph = document.add_paragraph("缺少样式定义的导入标题")
        paragraph._p.style = "ImportedHeading2"
        document.save(self.path)
        info = json.loads(self.ok(self.cli("docx-info", self.path)))
        self.assertEqual(info["headings"], [{"lvl": 2, "text": "缺少样式定义的导入标题"}])
        self.assertIn("## 缺少样式定义的导入标题", self.ok(self.cli("docx-text", self.path)))

    def test_bad_zip_and_incomplete_docx_fail_cleanly(self):
        self.path.write_bytes(b"not a ZIP document")
        with zipfile.ZipFile(self.root / "incomplete.docx", "w") as archive:
            archive.writestr("word/document.xml", "<broken>")
        for file in [self.path, self.root / "incomplete.docx", self.root / "missing.docx"]:
            for name in ["docx-info", "docx-text"]:
                with self.subTest(file=file.name, name=name):
                    result = self.cli(name, file)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, "")
                    self.assertTrue(result.stderr.startswith(name + ":"), result.stderr)
                    self.assertNotIn("Traceback", result.stderr)

    def test_corrupt_xml_inside_docx_package_fails_cleanly(self):
        document = Document()
        document.add_paragraph("用于损坏XML的合成正文")
        document.save(self.path)
        malformed = self.root / "malformed.docx"
        with zipfile.ZipFile(self.path) as source, zipfile.ZipFile(malformed, "w") as target:
            for entry in source.infolist():
                target.writestr(entry, b"<malformed>" if entry.filename == "word/document.xml" else source.read(entry.filename))
        for name in ["docx-info", "docx-text"]:
            result = self.cli(name, malformed)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")
            self.assertTrue(result.stderr.startswith(name + ":"), result.stderr)
            self.assertNotIn("Traceback", result.stderr)

    def test_invalid_ranges_are_clear_nonzero_errors(self):
        self.mixed_document()
        for args in [("nonnumeric",), (0,), (-1,), (3, 2), (1, 0), (1, "two"), (1, 2, 3)]:
            with self.subTest(args=args):
                result = self.cli("docx-text", self.path, *args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")
                self.assertNotEqual(result.stderr.strip(), "")
                self.assertNotIn("Traceback", result.stderr)

    def test_reading_is_nonmutating(self):
        self.mixed_document()
        before = (hashlib.sha256(self.path.read_bytes()).hexdigest(), self.path.stat().st_mtime_ns)
        self.ok(self.cli("docx-info", self.path))
        self.ok(self.cli("docx-text", self.path, 1, 4))
        self.assertEqual((hashlib.sha256(self.path.read_bytes()).hexdigest(), self.path.stat().st_mtime_ns), before)

    def test_unavailable_runtime_dependency_has_actionable_error(self):
        self.mixed_document()
        for name in ["docx-info", "docx-text"]:
            result = subprocess.run([sys.executable, "-S", str(SCRIPTS / name), str(self.path)], capture_output=True, text=True, timeout=15)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")
            self.assertIn("python-docx", result.stderr)
            self.assertNotIn("Traceback", result.stderr)


if __name__ == "__main__":
    unittest.main()
