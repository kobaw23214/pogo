import { describe, expect, it } from "vitest";
import type { MessageInputPart } from "@/api/types";
import { normalizeStarter, starterToWire } from "./studioPresentation";

describe("saved shortcut ordered content", () => {
  it("round trips text, repeated elements and persistent files through the Agent form", () => {
    const parts: MessageInputPart[] = [
      { type: "text", text: "1. 点击 " },
      { type: "element", text: '<page-element source="native" url="https://example.test/">\nbutton 登录\n</page-element>' },
      { type: "text", text: " 然后读取 " },
      { type: "file", path: "starter-assets/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/表格.xlsx", as_file: true },
    ];
    const card = { title: "固定流程", prompt: "1. 点击【页面元素】然后读取【表格.xlsx】", parts, input_layout: "inline" as const };
    const form = normalizeStarter(card);
    expect(starterToWire(form)).toEqual({ ...card, label: "", icon: "" });
    expect(form.parts).not.toBe(parts);
    expect(form.parts?.[0]).not.toBe(parts[0]);
    expect((starterToWire(form) as typeof card).parts).toEqual(parts);
  });

  it("does not collapse a card with only structured content into its fallback string", () => {
    const card = { prompt: "【数据.xlsx】", parts: [{ type: "file" as const, path: "starter-assets/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/数据.xlsx" }], input_layout: "top" as const };
    expect(starterToWire(normalizeStarter(card))).toMatchObject(card);
    expect(starterToWire(normalizeStarter("普通开场问题"))).toBe("普通开场问题");
  });
});
