import { inject, watchEffect, type ComputedRef, type Ref } from "vue";
import type { CiteSource } from "@/api/types";
import { citeFaviconKey, citeSourceFavicon } from "./citeFavicon";
import "./citeFavicon.css";

/** Patch only the reserved icon slot when browser metadata arrives. Replacing
 * the Markdown HTML would lose a reader's selection, focus or open popover. */
export function useInlineCiteFavicons(root: Ref<HTMLElement | null>, sources: () => CiteSource[] | undefined, rendered: ComputedRef<unknown>) {
  const cache = inject(citeFaviconKey, null);
  const failed = new Set<string>();
  watchEffect(() => {
    void rendered.value;
    const icons = new Map((sources() ?? []).map(source => [source.label, citeSourceFavicon(source, cache)]));
    for (const slot of root.value?.querySelectorAll<HTMLElement>(".cite-source-icon[data-cite-icon]") ?? []) {
      const icon = icons.get(slot.dataset.citeIcon ?? "");
      const current = slot.querySelector<HTMLImageElement>(".cite-favicon");
      if (current?.getAttribute("src") === icon) continue;
      current?.remove();
      slot.classList.remove("has-favicon");
      if (!icon || failed.has(icon)) continue;
      const image = document.createElement("img");
      image.className = "cite-favicon";
      image.alt = "";
      image.decoding = "async";
      image.onload = () => { if (image.parentElement === slot) slot.classList.add("has-favicon"); };
      image.onerror = () => {
        failed.add(icon);
        if (failed.size > 64) failed.delete(failed.values().next().value!);
        if (image.parentElement === slot) { image.remove(); slot.classList.remove("has-favicon"); }
      };
      image.src = icon;
      slot.append(image);
    }
  }, { flush: "post" });
}
