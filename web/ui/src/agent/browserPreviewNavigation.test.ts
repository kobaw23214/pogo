import { describe, expect, it } from "vitest";
import type { DockTab } from "@/components/dockSheets";
import { websiteBrowserRequestCurrent } from "./browserPreviewNavigation";

const site: DockTab = { id: "website:s1:ws_one", kind: "website", label: "Website", parentSession: "s1", webServiceId: "ws_one", closable: true };
const url = `http://192.168.1.2:19770/_pogo/web/${"a".repeat(48)}/`;
const request = { url, title: "Website", sessionId: "s1", serviceId: "ws_one" };
describe("website native browser entry", () => {
  it("accepts the current sheet's lifecycle URL and legacy domain grants", () => {
    expect(websiteBrowserRequestCurrent(request, "s1", [site])).toBe(true);
    expect(websiteBrowserRequestCurrent({ ...request, url: `https://${"a".repeat(48)}.web.example/` }, "s1", [site])).toBe(true);
  });
  it("rejects a stale conversation or a sheet removed while status was being checked", () => {
    expect(websiteBrowserRequestCurrent(request, "s2", [site])).toBe(false);
    expect(websiteBrowserRequestCurrent(request, null, [site])).toBe(false);
    expect(websiteBrowserRequestCurrent(request, "s1", [])).toBe(false);
    expect(websiteBrowserRequestCurrent({ ...request, serviceId: "ws_other" }, "s1", [site])).toBe(false);
  });
  it.each(["javascript:alert(1)", "file:///tmp/index.html", url + "?token=x", url + "#x", url.replace("http://", "http://secret@"), "http://example.com/", "http://example.com/_pogo/web/no/", url + "\n"])("rejects non-grant or altered URLs", candidate => {
    expect(websiteBrowserRequestCurrent({ ...request, url: candidate }, "s1", [site])).toBe(false);
  });
});
