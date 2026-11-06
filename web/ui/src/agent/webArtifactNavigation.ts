import type { InjectionKey } from "vue";
import type { OpenWebServiceRequest } from "@/api/webServices";
import type { WebsiteBrowserRequest } from "./browserPreviewNavigation";
import type { WebArtifactScope } from "./webArtifact";

/** Supplied by the current private conversation, never by saved Markdown. */
export interface WebArtifactNavigation {
  scope(): WebArtifactScope | null;
  openDock(request: OpenWebServiceRequest): void | Promise<void>;
  openBrowser(request: WebsiteBrowserRequest): void | Promise<void>;
}
export const webArtifactNavigationKey: InjectionKey<WebArtifactNavigation> = Symbol("webArtifactNavigation");
