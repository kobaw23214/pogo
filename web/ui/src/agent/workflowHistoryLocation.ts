/** Only explicit run selections change the deep link. Observation/reconnects
 * must leave it alone, while account and other page parameters ride along. */
export function workflowHistoryLocation(href: string, runId: string | null): string {
  const url = new URL(href);
  if (runId) url.searchParams.set("run", runId);
  else url.searchParams.delete("run");
  return url.href;
}
