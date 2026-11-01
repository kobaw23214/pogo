import { describe, expect, it } from "vitest";
import MarkdownIt from "markdown-it";
import { installMarkdownMath, renderMath } from "./markdownMath";

function engine() {
  const md = new MarkdownIt({ html: false });
  installMarkdownMath(md);
  return md;
}

describe("Markdown math", () => {
  it("renders inline and display formulas as accessible KaTeX", () => {
    const html = engine().render("Euler: $e^{i\\pi}+1=0$\n\n$$\nx=\\frac{-b\\pm\\sqrt{b^2-4ac}}{2a}\n$$");
    expect(html).toContain("katex");
    expect(html).toContain("katex-display");
    expect(html).toContain("<math");
  });

  it("does not treat ordinary currency as a formula", () => {
    const html = engine().render("总价是 $5 和 $10。 ");
    expect(html).not.toContain("katex");
    expect(html).toContain("$5 和 $10");
  });

  it("keeps invalid formulas and source HTML inert", () => {
    const html = engine().render("坏公式 $\\frac{$\n\n<img src=x onerror=alert(1)>");
    expect(html).toContain("math-inline-error");
    expect(html).not.toContain("<img");
    expect(() => renderMath("\\frac{", true)).toThrow();
  });

  it("keeps dollar signs in code spans and accepts adjacent formulas", () => {
    const html = engine().render("`$x$` $x$ 和 $y$；价格 $5.00 和 $10.00");
    expect(html).toContain("<code>$x$</code>");
    expect(html).toContain("katex");
    expect(html).toContain("$5.00 和 $10.00");
  });
});
