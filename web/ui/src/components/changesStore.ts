// The changes panel's freshness channel, in the fileTreeStore tradition.
//
// A settled write/edit/bash/subagent call may have changed the workspace in
// ways the journal will only see on the next read. Rather than push
// data, the stream bumps a tick and the panel — if it has data loaded —
// refetches over its normal endpoint. Debouncing is handled by the stream's
// tree scheduler this rides on.

import { ref } from "vue";

export const workspaceChangesTick = ref(0);

/** bumpWorkspaceChanges says "the workspace moved; refetch when visible". */
export function bumpWorkspaceChanges() {
  workspaceChangesTick.value += 1;
}
