package tools

import "strings"

// Recovery text explains a trusted executor failure. It never dispatches an
// action, changes a page binding or promotes page text into execution status.
func browserFailureRecovery(action, issue string) string {
	lower := strings.ToLower(issue)
	switch {
	case strings.Contains(issue, "TAB_CONTROL_CHANGED"), strings.Contains(issue, "BROWSER_CONTEXT_CHANGED"), strings.Contains(issue, "BROWSER_SESSION_EXPIRED"):
		return "Page identity or control changed. Read tabs once and use tab_id/context_ref/control_epoch from the same fresh row only if that page is still Agent-controlled. Do not drop the binding, automatically claim a human-controlled page, or replay a previous click or submission."
	case strings.Contains(issue, "BROWSER_TAB_CLOSED"), strings.Contains(lower, "no browser tabs"):
		return "Read tabs once; stop using the closed page's IDs and refs. If the authorized task still needs navigation, create a new Agent tab with tab_action:new and the intended original URL. Do not replay completed clicks or submissions."
	case strings.Contains(lower, "stale screenshot"), strings.Contains(lower, "screenshot version is stale"), strings.Contains(lower, "stale page version"):
		return "Obtain a fresh screenshot and inspect its pixels through the configured image path before choosing x/y/version. Do not reuse stale coordinates or replay completed batch steps."
	case strings.Contains(lower, "stale ref"), strings.Contains(lower, "stale element"), strings.Contains(lower, "not found in the current page snapshot"):
		return "Observe the selected page again and use a fresh ref/version from that same page. Do not guess a replacement target or replay completed batch steps."
	case strings.Contains(issue, "ERR_CONNECTION_TIMED_OUT"), strings.Contains(issue, "ERR_CONNECTION_REFUSED"), strings.Contains(issue, "ERR_NAME_NOT_RESOLVED"), strings.Contains(issue, "ERR_ADDRESS_UNREACHABLE"), strings.Contains(issue, "ERR_NETWORK_CHANGED"):
		return "The page route was not reachable. Inspect tabs and captured network diagnostics once. More waits, duplicate tabs or repeated navigation do not repair the route; report it as unverified until a relevant configuration or network state changes. Do not rewrite managed website addresses."
	case strings.Contains(lower, "intercepts pointer events"), strings.Contains(lower, "covered by dialog"):
		return "The target is obstructed. Inspect the current page and dialogs before choosing a new target. Do not automatically force-click, remove overlays or replay completed batch steps."
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"), strings.Contains(lower, "deadline exceeded"):
		if action == BrowserActionWait {
			return "The wait condition was not observed. Read the current snapshot or captured network once; a timeout does not prove that the page is empty or an action completed. Change the condition only after observing new state, and never repeat an uncertain write."
		}
		return "The operation or its observation exceeded the wait budget. Read tabs/current state once: navigation may have created a page and inputs may already have run. Do not open duplicate tabs or replay completed or uncertain actions; retry only after observing a relevant state change."
	default:
		return ""
	}
}
