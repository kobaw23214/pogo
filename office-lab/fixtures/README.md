# Synthetic Office fixtures

These scripts create **new test files** for the isolated browser prototype.
They do not read user documents, install desktop Office, or change Pogo services.
Keep generated DOCX/XLSX files and inspection reports under `.localagent/checks/`.

`generate.py` needs `python-docx` and `openpyxl`; `verify_exports.py` uses only the
Python standard library. The Codex bundled Python currently contains both
generation dependencies. No package installation is needed.

```sh
python3 pogo/office-lab/fixtures/generate.py \
  --output .localagent/checks/onlyoffice-prototype-20261012/fixtures
```

The directory contains `pogo-office-word.docx`, `pogo-office-excel.xlsx`, and
`manifest.json`. Word includes Chinese headings, a styled target paragraph,
table, header, and footer. Excel includes three worksheets, formatted sales
cells, a table, and formulas with a reference to another worksheet.

## Live editor sequence

1. Open the synthetic file in the browser editor.
2. In Word, enter the manifest's `word.human_marker` in the user-input area.
   In Excel, put `POGO_EXCEL_USER_EDIT_001` in `销售!E2`.
3. Have the Agent **read the current open editor before saving**. Capture that
   read separately; an exported file cannot prove the live read occurred.
4. Have the Agent replace the Word target with the manifest's `word.edited`.
   In Excel, change `销售!C2` from `3` to `5`, retaining `销售!D2 = B2*C2`.
   The visible totals should become `600` and `1060`.
5. Undo the Agent batch in the editor. The target must return to its original
   content/value while the earlier human marker remains. Do not undo by
   restoring the entire file from the fixture directory.
6. Apply the Agent edit again, save through the editor's real save callback,
   close/reopen, and inspect the saved export with this script.

```sh
python3 pogo/office-lab/fixtures/verify_exports.py \
  --baseline .localagent/checks/onlyoffice-prototype-20261012/fixtures \
  --word /path/to/saved-word.docx \
  --excel /path/to/saved-excel.xlsx \
  --phase edited \
  --require-human-word --require-human-excel --require-formula-cache \
  --output /path/to/export-inspection.json
```

Use `--phase undone` to check the state after native undo and saving. Omitting
the human flags is appropriate only for the original generated fixture.
The generated XLSX requests calculation on open but has **no precomputed
formula cache**. `--require-formula-cache` is deliberately mandatory for saved
Excel acceptance, so a successful write cannot hide missing formula results.

The inspection compares the selected content, target paragraph's effective
font/color/size/spacing, table text, header/footer text, sheet names, formulas,
selected cell styles, guarded values, and optional saved formula results.
It does not certify every Office feature, exact pagination, render quality,
live editing, Agent execution, user-input latency, or undo history. Those need
their own browser evidence. Engine exports may normalize OOXML identities;
the comparisons resolve style references rather than comparing ZIP hashes.
