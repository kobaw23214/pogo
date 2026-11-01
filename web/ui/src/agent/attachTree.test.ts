import { describe, expect, it } from "vitest";
import { attachTree, buildAttachTree, type AttachFile } from "./attachTree";

const f = (path: string, name?: string): AttachFile => ({ path, name: name ?? path.split("/").pop()! });

describe("buildAttachTree", () => {
  it("keeps loose files flat: uploads/ root hidden, no dir, chips path stays", () => {
    const t = buildAttachTree([f("uploads/a.pdf"), f("uploads/b.txt")]);
    expect(t.hasDir).toBe(false);
    expect(t.roots.map((r) => r.name)).toEqual(["a.pdf", "b.txt"]);
    expect(t.roots[0].path).toBe("uploads/a.pdf");
  });

  it("rebuilds one folder upload as a single dir node with a descendant count", () => {
    const t = buildAttachTree([
      f("uploads/02-计算机基础/CSAPP.pdf"),
      f("uploads/02-计算机基础/人月神话.pdf"),
      f("uploads/02-计算机基础/代码大全.pdf"),
      f("uploads/02-计算机基础/算法.pdf"),
    ]);
    expect(t.hasDir).toBe(true);
    expect(t.roots).toHaveLength(1);
    const dir = t.roots[0];
    expect(dir.dir).toBe(true);
    expect(dir.name).toBe("02-计算机基础");
    expect(dir.path).toBe("uploads/02-计算机基础");
    expect(dir.count).toBe(4);
    expect(dir.children).toHaveLength(4);
    // CJK collation order is ICU-dependent; assert the set, not the sequence.
    expect(new Set(dir.children.map((c) => c.name))).toEqual(
      new Set(["CSAPP.pdf", "人月神话.pdf", "代码大全.pdf", "算法.pdf"]),
    );
  });

  it("sorts a folder's files alphabetically (ASCII check on the ordering rule)", () => {
    const t = buildAttachTree([f("uploads/d/b10.txt"), f("uploads/d/b2.txt"), f("uploads/d/a.txt")]);
    expect(t.roots[0].children.map((c) => c.name)).toEqual(["a.txt", "b2.txt", "b10.txt"]);
  });

  it("mixes a folder and loose files: dir node first, loose files after", () => {
    const t = buildAttachTree([f("uploads/note.txt"), f("uploads/pack/a.png"), f("uploads/pack/sub/b.png")]);
    expect(t.hasDir).toBe(true);
    expect(t.roots.map((r) => [r.name, r.dir])).toEqual([
      ["pack", true],
      ["note.txt", false],
    ]);
    const pack = t.roots[0];
    expect(pack.count).toBe(2);
    expect(pack.children[0].name).toBe("sub");
    expect(pack.children[0].count).toBe(1);
    expect(pack.children[1].name).toBe("a.png");
  });

  it("keeps sibling folders from two picks as separate roots", () => {
    const t = buildAttachTree([f("uploads/a/x.txt"), f("uploads/b/y.txt")]);
    expect(t.roots.map((r) => r.name)).toEqual(["a", "b"]);
  });

  it("dedupes repeated paths", () => {
    const t = buildAttachTree([f("uploads/d/x.txt"), f("uploads/d/x.txt")]);
    expect(t.roots[0].count).toBe(1);
    expect(t.roots[0].children).toHaveLength(1);
  });

  it("hides nothing when paths share no head segment", () => {
    const t = buildAttachTree([f("uploads/x.txt"), f("other/y.txt")]);
    expect(t.hasDir).toBe(true);
    expect(t.roots.map((r) => r.name).sort()).toEqual(["other", "uploads"]);
  });

  it("handles a bare filename with no slash", () => {
    const t = buildAttachTree([f("plain.txt")]);
    expect(t.hasDir).toBe(false);
    expect(t.roots[0]).toMatchObject({ name: "plain.txt", path: "plain.txt", dir: false });
  });

  it("returns an empty tree for no files", () => {
    expect(buildAttachTree([])).toEqual({ roots: [], hasDir: false });
  });
});

describe("attachTree", () => {
  it("memoizes by array reference", () => {
    const files = [f("uploads/d/x.txt")];
    expect(attachTree(files)).toBe(attachTree(files));
  });
});
