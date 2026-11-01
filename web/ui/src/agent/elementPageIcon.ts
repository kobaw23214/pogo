import { browserFavicon } from "./browserFavicon";
import { citeFaviconURL, type CiteFavicons } from "./citeFavicon";

/** Display-only website identity. Missing bytes can be restored from the
 * conversation's saved exact-document archive, never a remote favicon URL. */
export function elementPageFavicon(url: string | undefined, icon: unknown, archive?: CiteFavicons | null): string | undefined {
  if (!url || !citeFaviconURL({ locator: url })) return;
  return browserFavicon(icon) ?? archive?.get({ locator: url });
}
