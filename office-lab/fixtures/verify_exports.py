#!/usr/bin/env python3
"""Inspect exported synthetic DOCX/XLSX files without opening a desktop Office app.

This proves selected OOXML content and styles survived a save. It does not prove
live-session reads, visible editing, native undo, or user input responsiveness.
The caller must capture those separately in the browser.
"""

import argparse
from decimal import Decimal
import json
import posixpath
import re
import zipfile
from pathlib import Path
from xml.etree import ElementTree as ET


NS = {"w": "http://schemas.openxmlformats.org/wordprocessingml/2006/main", "s": "http://schemas.openxmlformats.org/spreadsheetml/2006/main", "r": "http://schemas.openxmlformats.org/officeDocument/2006/relationships"}


def xml(archive, name):
    return ET.fromstring(archive.read(name))


def word_text(element):
    return "".join(node.text or "" for node in element.findall(".//w:t", NS))


def attributes(element):
    if element is None:
        return {}
    return {key.rsplit("}", 1)[-1]: value for key, value in element.attrib.items()}


def word_properties(parent, prefix):
    properties = {}
    if parent is None:
        return properties
    interesting = {"rFonts", "sz", "szCs", "color", "b", "i", "u"} if prefix == "r" else {"spacing", "jc", "ind"}
    for child in parent:
        name = child.tag.rsplit("}", 1)[-1]
        if name in interesting:
            values = attributes(child)
            if values:
                properties[name] = values
            elif name in {"b", "i", "u"}:
                properties[name] = {"val": "1"}
    return properties


def merge_word_properties(target, source):
    for name, values in source.items():
        target[name] = {**target.get(name, {}), **values}


def canonical_number(value):
    return format(Decimal(value).normalize(), "f") if value is not None else None


def word_snapshot(path, marker):
    with zipfile.ZipFile(path) as archive:
        root = xml(archive, "word/document.xml")
        styles_root = xml(archive, "word/styles.xml")
        styles = {node.get("{" + NS["w"] + "}styleId"): node for node in styles_root.findall("w:style", NS)}
        paragraphs = root.findall(".//w:body/w:p", NS)
        targets = [p for p in paragraphs if marker in word_text(p)]
        target = targets[0] if len(targets) == 1 else None
        style = {}
        if target is not None:
            ppr = target.find("w:pPr", NS)
            ref = ppr.find("w:pStyle", NS) if ppr is not None else None
            style_id = ref.get("{" + NS["w"] + "}val") if ref is not None else "Normal"
            chain, seen = [], set()
            while style_id in styles and style_id not in seen:
                seen.add(style_id)
                entry = styles[style_id]
                chain.append(entry)
                base = entry.find("w:basedOn", NS)
                style_id = base.get("{" + NS["w"] + "}val") if base is not None else None
            rpr = word_properties(styles_root.find("w:docDefaults/w:rPrDefault/w:rPr", NS), "r")
            pvalues = word_properties(styles_root.find("w:docDefaults/w:pPrDefault/w:pPr", NS), "p")
            for entry in reversed(chain):
                merge_word_properties(rpr, word_properties(entry.find("w:rPr", NS), "r"))
                merge_word_properties(pvalues, word_properties(entry.find("w:pPr", NS), "p"))
            merge_word_properties(pvalues, word_properties(ppr, "p"))
            runs = [run for run in target.findall(".//w:r", NS) if word_text(run)]
            effective_runs = []
            for run in runs:
                effective = dict(rpr)
                direct = word_properties(run.find("w:rPr", NS), "r")
                for key, value in direct.items():
                    effective[key] = {**effective.get(key, {}), **value}
                # Theme slots and language/proofing metadata are not selected
                # acceptance criteria. Explicit requested font/size/color are.
                fonts = effective.get("rFonts", {})
                color = effective.get("color", {}).get("val")
                effective_runs.append({"ascii_font": fonts.get("ascii"), "east_asia_font": fonts.get("eastAsia"), "size_half_points": canonical_number(effective.get("sz", {}).get("val")), "color": color.upper() if color else color, "bold": effective.get("b", {}).get("val", "0") not in ("0", "false"), "italic": effective.get("i", {}).get("val", "0") not in ("0", "false")})
            spacing = pvalues.get("spacing", {})
            style = {"runs": effective_runs, "spacing_after": canonical_number(spacing.get("after", "0")), "line_spacing": canonical_number(spacing.get("line", "240")), "line_rule": spacing.get("lineRule", "auto"), "alignment": pvalues.get("jc", {}).get("val", "left")}
        return {"paragraphs": [word_text(p) for p in paragraphs], "target_count": len(targets), "target": word_text(target) if target is not None else None, "target_style": style, "tables": [[word_text(cell) for cell in table.findall(".//w:tc", NS)] for table in root.findall(".//w:tbl", NS)], "headers": [word_text(xml(archive, name)) for name in archive.namelist() if name.startswith("word/header") and name.endswith(".xml")], "footers": [word_text(xml(archive, name)) for name in archive.namelist() if name.startswith("word/footer") and name.endswith(".xml")]}


def relationship_path(base, target):
    return posixpath.normpath(target.lstrip("/") if target.startswith("/") else posixpath.join(posixpath.dirname(base), target))


def excel_color(element, default=None):
    values = attributes(element)
    if "rgb" in values:
        return values["rgb"][-6:].upper()
    if values.get("theme") == "1" or values.get("indexed") == "64" or values.get("auto") == "1":
        return "000000"
    return values or default


def canonical_formula(value):
    if value is None:
        return None
    return re.sub(r"'([^']+)'!", r"\1!", value).replace(" ", "").lstrip("=")


def cell_position(reference):
    match = re.fullmatch(r"([A-Z]+)([1-9][0-9]*)", reference)
    if not match:
        raise ValueError("Unsupported shared-formula cell reference: " + reference)
    column = 0
    for letter in match[1]:
        column = column * 26 + ord(letter) - ord("A") + 1
    return column, int(match[2])


def column_letters(index):
    if index < 1 or index > 16384:
        raise ValueError("Shared formula shifted beyond the Excel column bounds")
    letters = ""
    while index:
        index, remainder = divmod(index - 1, 26)
        letters = chr(ord("A") + remainder) + letters
    return letters


def translate_shared_formula(formula, anchor, target):
    """Resolve conventional A1 references, leaving quoted text/names untouched."""
    anchor_column, anchor_row = cell_position(anchor)
    target_column, target_row = cell_position(target)
    delta_column, delta_row = target_column - anchor_column, target_row - anchor_row
    reference = re.compile(r"(?<![A-Za-z0-9_.])(\$?)([A-Z]{1,3})(\$?)([1-9][0-9]*)(?![A-Za-z0-9_(!])")

    def shift(match):
        column, row = cell_position(match[2] + match[4])
        if not match[1]:
            column += delta_column
        if not match[3]:
            row += delta_row
        if row < 1 or row > 1048576:
            raise ValueError("Shared formula shifted beyond the Excel row bounds")
        return match[1] + column_letters(column) + match[3] + str(row)

    parts = re.split(r'("(?:""|[^"])*"|\'(?:\'\'|[^\'])*\'(?=!))', formula)
    return "".join(part if index % 2 else reference.sub(shift, part) for index, part in enumerate(parts))


def excel_snapshot(path):
    with zipfile.ZipFile(path) as archive:
        workbook = xml(archive, "xl/workbook.xml")
        relations = {n.get("Id"): relationship_path("xl/workbook.xml", n.get("Target")) for n in xml(archive, "xl/_rels/workbook.xml.rels")}
        strings = ["".join(n.itertext()) for n in xml(archive, "xl/sharedStrings.xml").findall("s:si", NS)] if "xl/sharedStrings.xml" in archive.namelist() else []
        styles = xml(archive, "xl/styles.xml")
        fonts = styles.findall("s:fonts/s:font", NS)
        fills = styles.findall("s:fills/s:fill", NS)
        borders = styles.findall("s:borders/s:border", NS)
        xfs = styles.findall("s:cellXfs/s:xf", NS)
        formats = {"0": "General", "2": "0.00", "4": "#,##0.00"}
        formats.update({node.get("numFmtId"): node.get("formatCode") for node in styles.findall("s:numFmts/s:numFmt", NS)})
        cells, sheet_names = {}, []
        for sheet in workbook.findall("s:sheets/s:sheet", NS):
            name = sheet.get("name")
            sheet_names.append(name)
            tree = xml(archive, relations[sheet.get("{" + NS["r"] + "}id")])
            raw_cells = tree.findall(".//s:sheetData/s:row/s:c", NS)
            shared_formulas = {}
            for cell in raw_cells:
                node = cell.find("s:f", NS)
                if node is not None and node.get("t") == "shared" and node.text:
                    shared_formulas[node.get("si")] = (cell.get("r"), node.text)
            for cell in raw_cells:
                value_node = cell.find("s:v", NS)
                value = value_node.text if value_node is not None else None
                if cell.get("t") == "s" and value is not None:
                    value = strings[int(value)]
                elif cell.get("t") == "inlineStr":
                    value = "".join(n.text or "" for n in cell.findall(".//s:t", NS))
                elif cell.get("t") not in ("str", "e", "b") and value is not None:
                    value = float(value)
                    if value.is_integer():
                        value = int(value)
                formula_node = cell.find("s:f", NS)
                formula = formula_node.text if formula_node is not None else None
                if formula_node is not None and formula_node.get("t") == "shared" and not formula:
                    shared = shared_formulas.get(formula_node.get("si"))
                    if shared:
                        formula = translate_shared_formula(shared[1], shared[0], cell.get("r"))
                xf = xfs[int(cell.get("s", "0"))]
                font = fonts[int(xf.get("fontId", "0"))]
                fill = fills[int(xf.get("fillId", "0"))].find("s:patternFill", NS)
                border = borders[int(xf.get("borderId", "0"))]
                font_name = font.find("s:name", NS)
                font_size = font.find("s:sz", NS)
                bold = font.find("s:b", NS)
                style = {"font": font_name.get("val") if font_name is not None else None, "size": canonical_number(font_size.get("val")) if font_size is not None else None, "bold": bold is not None and bold.get("val", "1") not in ("0", "false"), "font_color": excel_color(font.find("s:color", NS), "000000"), "fill": {"pattern": (fill.get("patternType") or "none") if fill is not None else "none", "fg": excel_color(fill.find("s:fgColor", NS)) if fill is not None else None}, "format": formats.get(xf.get("numFmtId", "0"), xf.get("numFmtId")), "border": {edge: attributes(border.find("s:" + edge, NS)) for edge in ["left", "right", "top", "bottom"]}}
                cells[name + "!" + cell.get("r")] = {"value": value, "formula": formula, "style": style}
        return {"sheet_names": sheet_names, "cells": cells}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True, help="Directory generated by generate.py, including manifest.json")
    parser.add_argument("--word", type=Path)
    parser.add_argument("--excel", type=Path)
    parser.add_argument("--phase", choices=["original", "edited", "undone"], default="original")
    parser.add_argument("--require-human-word", action="store_true")
    parser.add_argument("--require-human-excel", action="store_true")
    parser.add_argument("--require-formula-cache", action="store_true", help="Require saved Excel formula values to be recalculated")
    parser.add_argument("--render-profile", choices=["strict", "light"], default="strict", help="Light allows verified white appearance for indexed 65 while preserving an explicit cross-theme fidelity boundary")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    manifest = json.loads((args.baseline / "manifest.json").read_text(encoding="utf-8"))
    checks = []
    boundaries = []

    def check(name, passed, detail=None):
        checks.append({"name": name, "passed": bool(passed), **({"detail": detail} if detail is not None else {})})

    if args.word:
        baseline = word_snapshot(args.baseline / manifest["files"]["word"]["name"], manifest["word"]["target_marker"])
        current = word_snapshot(args.word, manifest["word"]["target_marker"])
        check("word.one-target", current["target_count"] == 1)
        check("word.target-content", current["target"] == manifest["word"]["edited" if args.phase == "edited" else "original"], current["target"])
        check("word.guarded-paragraphs", all(p in current["paragraphs"] for p in manifest["word"]["guards"]))
        check("word.table-text", current["tables"] == baseline["tables"])
        check("word.headers", current["headers"] == baseline["headers"])
        check("word.footers", current["footers"] == baseline["footers"])
        base_style = baseline["target_style"]
        current_style = current["target_style"]
        check("word.target-run-style", bool(current_style.get("runs")) and all(style == base_style["runs"][0] for style in current_style.get("runs", [])), current_style.get("runs"))
        check("word.target-paragraph-style", {k: v for k, v in current_style.items() if k != "runs"} == {k: v for k, v in base_style.items() if k != "runs"}, {k: v for k, v in current_style.items() if k != "runs"})
        if args.require_human_word:
            check("word.human-edit-preserved", any(manifest["word"]["human_marker"] in p for p in current["paragraphs"]))
    if args.excel:
        baseline = excel_snapshot(args.baseline / manifest["files"]["excel"]["name"])
        current = excel_snapshot(args.excel)
        check("excel.sheet-names", current["sheet_names"] == manifest["excel"]["sheet_names"])
        target = manifest["excel"]["target"]
        check("excel.target-value", current["cells"].get(target, {}).get("value") == manifest["excel"]["edited" if args.phase == "edited" else "original"])
        for ref, formula in manifest["excel"]["formulas"].items():
            actual = current["cells"].get(ref, {}).get("formula")
            check("excel.formula." + ref, canonical_formula(actual) == canonical_formula(formula), actual)
        for ref in ["销售!A1", "销售!B2", "销售!C2", "销售!D2", "汇总!B2"]:
            actual_style = current["cells"].get(ref, {}).get("style")
            expected_style = baseline["cells"][ref]["style"]
            compared_style = dict(actual_style) if actual_style else None
            if actual_style and actual_style["font_color"] == {"indexed": "65"} and expected_style["font_color"] == "FFFFFF":
                boundaries.append({"cell": ref, "property": "font_color", "original": "RGB FFFFFF", "exported": "indexed 65 / system background", "profile": args.render_profile, "appearance": "White only within the verified light rendering profile; not guaranteed across system themes", "exact_encoding_preserved": False, "cross_theme_fidelity": "not verified", "reference": "https://epplussoftware.com/docs/7.5/api/OfficeOpenXml.Style.ExcelIndexedColor.html"})
                if args.render_profile == "light":
                    compared_style["font_color"] = "FFFFFF"
            check("excel.style." + ref, compared_style == expected_style, actual_style)
        for ref in ["销售!A2", "销售!B2", "销售!A3", "销售!B3", "销售!C3", "销售!A4", "销售!B4", "销售!C4", "汇总!B4", "说明!B2"]:
            check("excel.guarded-value." + ref, current["cells"].get(ref, {}).get("value") == baseline["cells"][ref]["value"])
        if args.require_human_excel:
            check("excel.human-edit-preserved", current["cells"].get(manifest["excel"]["human_target"], {}).get("value") == manifest["excel"]["human_marker"])
        if args.require_formula_cache:
            for ref, value in manifest["excel"]["calculated_edited" if args.phase == "edited" else "calculated_original"].items():
                check("excel.recalculated-cache." + ref, current["cells"].get(ref, {}).get("value") == value, current["cells"].get(ref, {}).get("value"))
    if not args.word and not args.excel:
        parser.error("provide --word or --excel")
    result = {"kind": "synthetic-ooxml-export-inspection", "phase": args.phase, "acceptance_scope": "Only the listed exported-file properties; live editor, agent, undo and visual behavior require separate browser evidence", "render_profile": args.render_profile, "fidelity_boundaries": boundaries, "passed": all(item["passed"] for item in checks), "checks": checks}
    rendered = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(rendered, encoding="utf-8")
        print(json.dumps({"passed": result["passed"], "phase": args.phase, "checks": len(checks), "failed": [item["name"] for item in checks if not item["passed"]], "fidelity_boundaries": len(boundaries), "render_profile": args.render_profile, "report": str(args.output.resolve())}, ensure_ascii=False))
    else:
        print(rendered, end="")
    raise SystemExit(0 if result["passed"] else 1)


if __name__ == "__main__":
    main()
