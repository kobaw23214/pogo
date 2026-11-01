#!/usr/bin/env python3
"""Create synthetic Office files for the isolated ONLYOFFICE lab.

Requires python-docx and openpyxl. Nothing is read from a user workspace.
The generated binaries belong in an ignored checks directory, not Git.
"""

import argparse
import hashlib
import json
from pathlib import Path

from docx import Document
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.oxml import OxmlElement
from docx.oxml.ns import qn
from docx.shared import Inches, Pt, RGBColor
from openpyxl import Workbook
from openpyxl.styles import Alignment, Border, Font, PatternFill, Side
from openpyxl.worksheet.table import Table, TableStyleInfo


WORD_ORIGINAL = "POGO_WORD_TARGET_001：这是等待 Agent 更新的正文，当前状态为草稿。"
WORD_EDITED = "POGO_WORD_TARGET_001：Agent 已更新正文，并保留原来的字体和段落样式。"
WORD_HUMAN = "POGO_WORD_USER_EDIT_001：这句话由用户在当前编辑器中输入，尚未保存。"
WORD_GUARDS = ["Pogo Office 浏览器编辑验证", "原始说明：该文档是人工构造的隔离样本，不包含用户资料。", "结束说明：此段应在 Agent 修改、撤销和保存后保持完整。"]


def word_fixture(path):
    document = Document()
    section = document.sections[0]
    section.header.paragraphs[0].text = "Pogo Office 隔离原型 · 仅测试样本"
    section.footer.paragraphs[0].text = "保存后请核对页眉、页脚、正文和表格。"
    section.top_margin = section.bottom_margin = Inches(0.8)
    normal = document.styles["Normal"]
    normal.font.name = "Arial"
    normal.font.size = Pt(11)
    normal.element.get_or_add_rPr().rFonts.set(qn("w:eastAsia"), "SimSun")
    target_style = document.styles.add_style("PogoTargetBody", 1)
    target_style.base_style = normal
    target_style.font.name = "Arial"
    target_style.font.size = Pt(11)
    target_style.font.color.rgb = RGBColor.from_string("234E70")
    target_style.element.get_or_add_rPr().rFonts.set(qn("w:eastAsia"), "SimSun")
    target_style.paragraph_format.space_after = Pt(6)
    target_style.paragraph_format.line_spacing = 1.2
    document.add_heading(WORD_GUARDS[0], 0)
    document.add_paragraph(WORD_GUARDS[1])
    document.add_heading("一、同一份文档的可视化修改", level=1)
    document.add_paragraph(WORD_ORIGINAL, style=target_style)
    document.add_paragraph("用户输入区：请在这里手动写一句话，供 Agent 读取未保存内容。")
    document.add_heading("二、结构与格式保留", level=1)
    table = document.add_table(rows=1, cols=3)
    table.style = "Table Grid"
    headers = ["检查项目", "原始状态", "预期结果"]
    for cell, value in zip(table.rows[0].cells, headers):
        cell.text = value
        cell.paragraphs[0].runs[0].bold = True
        shading = OxmlElement("w:shd")
        shading.set(qn("w:fill"), "E8EEF5")
        cell._tc.get_or_add_tcPr().append(shading)
    for values in [("页眉 / 页脚", "存在", "保存后仍存在"), ("正文样式", "蓝色正文，11 pt", "Agent 编辑后保留"), ("中文表格", "三列", "撤销及重开后完整")]:
        for cell, value in zip(table.add_row().cells, values):
            cell.text = value
    document.add_paragraph(WORD_GUARDS[2])
    for paragraph in section.header.paragraphs + section.footer.paragraphs:
        paragraph.alignment = WD_ALIGN_PARAGRAPH.CENTER
    document.core_properties.title = "Pogo Office 隔离 Word 样本"
    document.core_properties.author = "Pogo Office Lab"
    document.save(path)


def excel_fixture(path):
    workbook = Workbook()
    sales = workbook.active
    sales.title = "销售"
    sales.append(["产品", "单价", "数量", "金额", "备注"])
    for row in [("键盘", 120, 3, "=B2*C2", "原始备注"), ("鼠标", 85, 4, "=B3*C3", "保持此行"), ("扩展坞", 60, 2, "=B4*C4", "保持此行")]:
        sales.append(row)
    summary = workbook.create_sheet("汇总")
    for row in [("指标", "数值"), ("销售总额", "=SUM(销售!D2:D4)"), ("产品数", "=COUNTA(销售!A2:A4)"), ("说明", "POGO_EXCEL_GUARD_001")]:
        summary.append(row)
    notes = workbook.create_sheet("说明")
    for row in [("项目", "内容"), ("样本", "人工构造的隔离文件，不包含用户资料。"), ("用户输入标记", "请在销售表 E2 输入 POGO_EXCEL_USER_EDIT_001。"), ("Agent 修改", "将销售表 C2 从 3 改为 5，D2 仍使用公式，汇总随之重算。")]:
        notes.append(row)
    thin = Side(style="thin", color="D4DCE6")
    for sheet in workbook:
        sheet.freeze_panes = "A2"
        sheet.sheet_view.showGridLines = True
        for row in sheet:
            for cell in row:
                cell.font = Font(name="Arial", size=11)
                cell.alignment = Alignment(vertical="center")
                cell.border = Border(left=thin, right=thin, top=thin, bottom=thin)
        for cell in sheet[1]:
            cell.font = Font(name="Arial", size=11, bold=True, color="FFFFFF")
            cell.fill = PatternFill("solid", fgColor="234E70")
        sheet.row_dimensions[1].height = 24
        sheet.column_dimensions["A"].width = 20
        sheet.column_dimensions["B"].width = 22 if sheet is not notes else 84
    sales.column_dimensions["C"].width = 14
    sales.column_dimensions["D"].width = 20
    sales.column_dimensions["E"].width = 38
    for row in sales.iter_rows(min_row=2, max_row=4):
        row[1].number_format = '#,##0.00'
        row[3].number_format = '#,##0.00'
    summary["B2"].number_format = '#,##0.00'
    table = Table(displayName="PogoSales", ref="A1:E4")
    table.tableStyleInfo = TableStyleInfo(name="TableStyleMedium2", showFirstColumn=False, showLastColumn=False, showRowStripes=True, showColumnStripes=False)
    sales.add_table(table)
    workbook.calculation.fullCalcOnLoad = True
    workbook.calculation.forceFullCalc = True
    workbook.save(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    files = {"word": args.output / "pogo-office-word.docx", "excel": args.output / "pogo-office-excel.xlsx"}
    word_fixture(files["word"])
    excel_fixture(files["excel"])
    manifest = {
        "synthetic": True,
        "purpose": "ONLYOFFICE live-session editing and exported-file inspection; generated files alone are not acceptance evidence",
        "files": {kind: {"name": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()} for kind, path in files.items()},
        "word": {"target_marker": "POGO_WORD_TARGET_001", "original": WORD_ORIGINAL, "edited": WORD_EDITED, "human_marker": WORD_HUMAN, "guards": WORD_GUARDS},
        "excel": {"sheet_names": ["销售", "汇总", "说明"], "target": "销售!C2", "original": 3, "edited": 5, "human_marker": "POGO_EXCEL_USER_EDIT_001", "human_target": "销售!E2", "formulas": {"销售!D2": "B2*C2", "销售!D3": "B3*C3", "销售!D4": "B4*C4", "汇总!B2": "SUM(销售!D2:D4)", "汇总!B3": "COUNTA(销售!A2:A4)"}, "calculated_original": {"销售!D2": 360, "销售!D3": 340, "销售!D4": 120, "汇总!B2": 820, "汇总!B3": 3}, "calculated_edited": {"销售!D2": 600, "销售!D3": 340, "销售!D4": 120, "汇总!B2": 1060, "汇总!B3": 3}},
    }
    (args.output / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output.resolve()), "files": [p.name for p in files.values()], "synthetic": True}, ensure_ascii=False))


if __name__ == "__main__":
    main()
