import { detectLanguage } from "./highlight";

export type InformationPreviewKind = "image" | "pdf" | "text" | "office" | "large" | "unsupported";

/** Use the same extension hint and tokenizer as the workspace's CodeBlock. */
export function informationFileLanguage(name: string): string {
  const base = name.split(/[\\/]/).pop() || "";
  const dot = base.lastIndexOf(".");
  return dot > 0 && dot < base.length - 1 ? base.slice(dot + 1).toLowerCase() : "";
}

export function informationFilePreview(name: string, mime: string, size: number, nativeOffice = false): InformationPreviewKind {
  const type = mime.split(";", 1)[0]!.trim().toLowerCase();
  const language = informationFileLanguage(name);
  // The immutable Office viewer streams the pinned source server-side and
  // applies its own document limit. Never download an Office package as text
  // or apply the ordinary image/PDF Blob preview's 20 MiB limit to it.
  // Shortcut attachments do not have a file-entry viewer capability, so only
  // the entry preview host opts into this branch.
  if (nativeOffice && (language === "docx" || language === "xlsx")) return "office";
  let format: InformationPreviewKind;
  if (type.startsWith("image/") && !type.includes("svg")) format = "image";
  else if (type === "application/pdf" || language === "pdf") format = "pdf";
  // No second language registry: new languages added to the shared tokenizer
  // become previewable here too. SVG/XML remains inert source text, not an image.
  else if (type.startsWith("text/") || detectLanguage(language) !== "plain" || /^(?:text|txt|csv|tsv|log)$/.test(language)) format = "text";
  else return "unsupported";
  return size > (format === "text" ? 2 : 20) * 1024 ** 2 ? "large" : format;
}
