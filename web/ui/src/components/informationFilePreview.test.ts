import { describe, expect, it } from "vitest";
import { fileIcon } from "./fileIcons";
import { detectLanguage, tokenize } from "./highlight";
import { informationFileLanguage, informationFilePreview } from "./informationFilePreview";

describe("information file preview", () => {
  it("reuses the workspace tokenizer and file-type icon for uploaded source files", () => {
    for (const [name, language] of [["test.PY", "python"], ["main.go", "go"], ["app.ts", "ts"], ["data.json", "json"], ["deploy.yml", "yaml"], ["run.bash", "shell"], ["app.properties", "properties"]]) {
      expect(informationFilePreview(name!, "application/octet-stream", 100)).toBe("text");
      expect(detectLanguage(informationFileLanguage(name!))).toBe(language);
      expect(fileIcon(name!)).not.toEqual(fileIcon("unknown.bin"));
    }
    const source = "def run(value):\n    return True\n";
    const tokens = tokenize(source, detectLanguage(informationFileLanguage("release_test.py")));
    expect(tokens.filter(token => token.kind === "keyword").map(token => token.text)).toEqual(["def", "return", "True"]);
    expect(tokens.map(token => token.text).join("")).toBe(source);
  });

  it("retains the text and visual limits and never mistakes binary Office for text", () => {
    const mib = 1024 ** 2;
    expect(informationFilePreview("source.py", "text/plain", 2 * mib)).toBe("text");
    expect(informationFilePreview("source.py", "text/plain", 2 * mib + 1)).toBe("large");
    expect(informationFilePreview("report.pdf", "application/pdf", 20 * mib)).toBe("pdf");
    expect(informationFilePreview("photo.png", "image/png", 20 * mib + 1)).toBe("large");
    expect(informationFilePreview("report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", 100)).toBe("unsupported");
    expect(informationFilePreview("binary", "application/octet-stream", 100)).toBe("unsupported");
  });

  it("routes Word and Excel directly to the immutable Office viewer, including large sources and generic MIME types", () => {
    for (const name of ["员工名单.xlsx", "report.DOCX"]) {
      expect(informationFilePreview(name, "application/octet-stream", 94 * 1024 ** 2, true)).toBe("office");
      expect(informationFilePreview(name, "text/plain", 100, true)).toBe("office");
    }
    // The server owns the Office size limit; do not fetch even oversized files
    // into a client Blob merely to discover that they cannot be opened.
    expect(informationFilePreview("large.xlsx", "application/octet-stream", 300 * 1024 ** 2, true)).toBe("office");
    for (const name of ["legacy.doc", "legacy.xls", "slides.pptx", "book.xlsm"]) {
      expect(informationFilePreview(name, "application/octet-stream", 100, true)).toBe("unsupported");
    }
  });

  it("keeps SVG inert and allows plain-text MIME without inventing a language", () => {
    expect(informationFilePreview("image.svg", "image/svg+xml", 100)).toBe("text");
    expect(informationFilePreview("README", "Text/Plain; charset=utf-8", 100)).toBe("text");
    expect(informationFileLanguage("README")).toBe("");
    expect(informationFilePreview("data.csv", "application/octet-stream", 100)).toBe("text");
    expect(informationFilePreview("report.bin", "application/octet-stream", 100)).toBe("unsupported");
  });
});
