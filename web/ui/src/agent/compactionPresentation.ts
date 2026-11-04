export function compactionReduction(beforeTokens: number, afterTokens: number): number | null {
  if (!Number.isFinite(beforeTokens) || !Number.isFinite(afterTokens) || beforeTokens <= 0 || afterTokens < 0) return null;
  return Math.max(0, Math.min(100, Math.round((1 - afterTokens / beforeTokens) * 100)));
}

export function compactionTokens(value: number): string {
  const n = Math.max(0, Math.round(value));
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n % 1_000_000 === 0 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}K`;
  return n.toLocaleString("en-US");
}

export function compactionSummaryExcerpt(summary: string, maxLength = 144): string {
  for (const source of summary.replace(/\r/g, "").split("\n")) {
    const line = source.trim();
    if (!line || /^#{1,6}\s+/.test(line) || /^<\/?[a-z_]+>$/i.test(line)) continue;
    const text = line.replace(/^[-*+]\s+/, "").replace(/^\d+[.)]\s+/, "").replace(/[*_`~]/g, "").trim();
    if (!text || (text.length < 40 && /[:：]$/.test(text))) continue;
    if (text.length <= maxLength) return text;
    const cut = text.slice(0, maxLength - 1);
    const space = cut.lastIndexOf(" ");
    return `${(space > maxLength * 0.65 ? cut.slice(0, space) : cut).trim()}…`;
  }
  return "";
}
