// The optimistic bubble's upload card, keyed by the message's client id in
// AgentView's uploadStates record. The bar takes the MAX of bytes and file
// counts (see AskUploadCard): bytes drive it only when the network is the
// bottleneck — against a local proxy the request body is buffered instantly,
// xhr.upload.onprogress never fires mid-file, and file completions are the
// only measure that always ticks. phase flips to "sending" once every file
// has landed: the bar is full but the message POST is still out, which is its
// own (short) wait and says so instead of looking stuck at 100%.
export interface UploadCardState {
  /** Sum of every pending file's byte size. */
  total: number;
  /** Bytes acknowledged in flight so far (per-file XHR progress, summed). */
  loaded: number;
  /** Files to upload. */
  count: number;
  /** Files fully uploaded. */
  done: number;
  phase: "uploading" | "sending";
}
