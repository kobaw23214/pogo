// Mirror of web/wire.go. That file is the source of truth; when the two
// disagree, it wins.
import type { WorkflowAuthoringDetails } from "@/agent/workflowAuthoringReceipt";
import type { AgentWorkflowDetails } from "@/agent/agentWorkflowNavigation";

// Built-in tool names. Delegated agents cannot delegate further; external MCP
// tools also travel over the wire using their server-provided names.
export type ToolName =
  | "read"
  | "ls"
  | "find"
  | "grep"
  | "write"
  | "edit"
  | "bash"
  | "browser"
  | "shortcut_query"
  | "shortcut_manage"
  | "shortcut_search"
  | "shortcut_read"
  | "shortcut_create"
  | "shortcut_list"
  | "shortcut_folder_create"
  | "shortcut_update"
  | "shortcut_folder_rename"
  | "shortcut_move"
  | "shortcut_delete"
  | "shortcut_merge"
  | "skill_search"
  | "skill_read"
  | "skill_save"
  | "background_task"
  | "workflow_discover"
  | "workflow_read"
  | "workflow_validate"
  | "workflow_edit"
  | "workflow_test"
  | "workflow_run"
  | "workflow_publish"
  | "agent_workflow_read"
  | "agent_workflow_edit"
  | "agent_workflow_enable"
  | "agent_workflow_run"
  | "watch"
  | "watch_test"
  | "watch_decide"
  | "subagent"
  | "request_user_input"
  | "request_user_input_async"
  | "subagent_tasks"
  | "todo";

export type Block =
  | { type: "text"; text: string; input_element?: boolean; input_element_favicon?: string; input_skill?: string; input_office?: OfficeReferenceSnapshot; input_shortcut?: { id: string; title: string; text: string; icon?: string; language?: string; parts?: MessageInputPart[]; block_count?: number } }
  // duration_ms is how long the thinking took to stream, measured server-side;
  // absent on transcripts recorded before the field existed.
  | { type: "thinking"; text: string; duration_ms?: number }
  | { type: "tool_use"; id: string; name: ToolName; input?: unknown; input_invalid?: boolean; input_error?: string }
  // tool_result never appears in messages: the server keys results out into a
  // separate table so replayed history and live rendering have one shape.
  | { type: "tool_result"; tool_use_id: string; text?: string; is_error?: boolean }
  // A user-attached image, persisted by workspace path. The bytes live in the
  // session's uploads/ directory, fetched through the files endpoints;
  // image_alt is what a text-only model was told about it (the vision
  // helper's description).
  | {
      type: "image";
      image_ref: string;
      image_mime?: string;
      image_name?: string;
      image_alt?: string;
      image_w?: number;
      image_h?: number;
    }
  // A non-image attachment the model reads by path: the transcript note names
  // it, the read tools and the docs skill do the reading.
  | {
      type: "file";
      file_path: string;
      file_name?: string;
      file_size?: number;
      file_kind?: string;
    };

/** An uploaded attachment as the session endpoint reports it. */
export interface AttachmentMeta {
  path: string;
  name: string;
  kind: "image" | "pdf" | "word" | "excel" | "csv" | "text" | string;
  mime: string;
  size: number;
  w?: number;
  h?: number;
  created: string;
}

export interface Message {
  id: string;
  role: "user" | "assistant";
  content: Block[];
  /** When the message was recorded, epoch ms. Absent on very old transcripts. */
  ts?: number;
  /** Server-authenticated follow-up identity; retained in transcript replay. */
  inbox_id?: string;
  run_id?: string;
  steering?: boolean;
  input_layout?: InputLayout;
  answer_review?: AnswerReview;
}

/** Server-owned lifecycle of an optional final-answer review, not model prose. */
export interface AnswerReview {
  kind: "citation" | "quote";
  state: "running" | "completed" | "fallback" | "cancelled";
  turn: number;
  started_at: number;
  finished_at?: number;
  duration_ms?: number;
  reason?: string;
  message_id?: string;
}

export interface OfficeReferenceSnapshot {
  path: string; title: string; editor: "word" | "cell"; text: string;
  sheet?: string; range?: string; canvas_version: string; dirty?: boolean; dirty_epoch?: number;
  captured_at?: string; source_session?: string;
  locator?: { kind: "text"; exact: string; prefix?: string; suffix?: string };
  /** Saved library selection, never a live editor claim or workspace path grant. */
  information?: { entry_id: string; version: number; snapshot_sha256: string };
}
export type InputLayout = "top" | "inline";
/** Restricted user input only; these are never arbitrary model/tool blocks. */
export type MessageInputPart =
  | { type: "text"; text: string }
  | { type: "skill"; name: string }
  | { type: "shortcut"; shortcut_id: string; title: string; text: string; icon?: string; language?: string; parts?: MessageInputPart[] }
  | { type: "office"; office: OfficeReferenceSnapshot }
  | { type: "element"; text: string; favicon?: string }
  | { type: "file"; path: string; as_file?: boolean };

export interface InboxItem {
  id: string;
  text: string;
  mode: "queued" | "steer";
  status: "queued" | "steering" | "delivered" | "withdrawn";
  revision: number;
  created_at: number;
  updated_at: number;
  run_id?: string;
  message_id?: string;
  note?: string;
  parts?: MessageInputPart[];
  input_layout?: InputLayout;
}

export interface ReadDetails {
  path: string;
  offset?: number;
  limit?: number;
  total_lines: number;
  shown_lines: number;
  first_line: number;
  truncated?: boolean;
  truncated_by?: string;
}

/** One file inside a multi-file read. `error` replaces the counts when it failed. */
export interface ReadFileDetails {
  path: string;
  total_lines?: number;
  shown_lines?: number;
  truncated?: boolean;
  truncated_by?: string;
  error?: string;
  /**
   * Byte range of this file's content inside the result text, excluding the header
   * and the truncation note. Absent for a path that could not be read, and for
   * transcripts recorded before the fields existed. See ReadFileDetails in
   * tools/result.go for why the body is located this way rather than parsed out.
   */
  body_offset?: number;
  body_length?: number;
}

/**
 * A read of several files in one call.
 *
 * Deliberately without a top-level `total_lines`, because that field is how a read
 * result is discriminated from this one (isReadDetails) and ReadResult draws exactly
 * one file. The shape is the Go type's, tags and all; see ReadManyDetails there for
 * why the budget is split and why one unreadable path does not fail the call.
 */
export interface ReadManyDetails {
  files: ReadFileDetails[];
}

export interface LsDetails {
  path: string;
  entries: number;
  dirs?: number;
  files?: number;
  total?: number;
  truncated?: boolean;
  entry_limited?: boolean;
}

export interface WriteDetails {
  path: string;
  bytes: number;
  created: boolean;
  diff?: string;
  patch?: string;
  added?: number;
  removed?: number;
}

export interface EditDetails {
  path: string;
  edits: number;
  diff: string;
  patch: string;
  first_changed_line?: number;
  added: number;
  removed: number;
}

export interface BashDetails {
  command: string;
  /** Where it ran, present only when that was not the session's own directory. */
  workdir?: string;
  /** A background acknowledgement has no exit code until the process settles. */
  exit_code?: number;
  task_id?: string;
  background?: boolean;
  status?: ShellJobStatus;
  /** Missing on a retained completion receipt that does not include elapsed time. */
  duration_ms?: number;
  timed_out?: boolean;
  truncated?: boolean;
  total_lines?: number;
  full_output_path?: string;
  /** The command ran inside the OS sandbox (bubblewrap): filesystem read-only
   * outside the workspace/HOME/TMPDIR, no network. Shown as a fact of the run. */
  sandboxed?: boolean;
}

export type ShellJobStatus = "queued" | "running" | "completed" | "failed" | "cancelled" | "timed_out" | "interrupted";

/** A host-owned command in this conversation, independent of any Agent turn. */
export interface ShellJobRecord {
  task_id: string;
  command: string;
  workdir: string;
  description?: string;
  status: ShellJobStatus;
  started_at: number;
  ended_at?: number;
  exit_code?: number;
  error?: string;
  output_tail: string;
  output_bytes: number;
  output_truncated: boolean;
  revision: number;
  background: boolean;
  persistence_error?: string;
}

export interface ShellJobOutput {
  task_id: string;
  output: string;
  offset: number;
  next_offset: number;
  total_bytes: number;
  truncated: boolean;
  status: ShellJobStatus;
  eof: boolean;
}

export interface FindDetails {
  pattern: string;
  path?: string;
  matches: number;
  // scanned is how many entries were visited. "No matches" after 3 entries and
  // after 200,000 mean different things.
  scanned: number;
  truncated?: boolean;
  limit_hit?: boolean;
}

export interface GrepDetails {
  pattern: string;
  path?: string;
  include?: string;
  matches: number;
  // files is how many distinct files contained a match.
  files: number;
  scanned: number;
  skipped_binary?: number;
  truncated?: boolean;
  limit_hit?: boolean;
}

/**
 * MCPDetails accompanies a call to an external MCP server's tool. Mirror of
 * tools.MCPDetails; populated even when the call fails, with the same json
 * names as BashDetails where the question is the same. The wire name of the
 * call is mcp_<server>_<tool> (possibly hash-truncated), so server/tool here
 * are the trustworthy identities — never parsed back out of the name.
 */
export interface MCPDetails {
  server: string;
  tool: string;
  duration_ms?: number;
  timed_out?: boolean;
  truncated?: boolean;
  full_output_path?: string;
  /** The call's structured result, verbatim; absent when the tool returned none. */
  structured_content?: unknown;
  /** Display metadata echoed from the tool's declaration; all server-provided. */
  title?: string;
  description?: string;
  annotations?: MCPAnnotations;
  input_schema?: Record<string, unknown>;
  /**
   * The call's typed content blocks — the structured copy of what the result
   * text flattens into placeholders. Absent on failure paths and on
   * transcripts recorded before the field existed.
   */
  content?: MCPContentBlock[];
}

/** The display copy of the server's annotations — untrusted hints, badged only. */
export interface MCPAnnotations {
  read_only_hint?: boolean;
  destructive_hint?: boolean;
  idempotent_hint?: boolean;
  open_world_hint?: boolean;
}

/**
 * One typed content block of an MCP call's result. data is base64 and present
 * only up to the server's inline cap (512 KiB); past it the block is metadata
 * plus omitted, matching the placeholder the model saw.
 */
export interface MCPContentBlock {
  type: string; // "text" | "image" | "audio" | "resource" | "resource_link" | "unknown"
  text?: string;
  mime_type?: string;
  data?: string;
  uri?: string;
  name?: string;
  size?: number;
  omitted?: boolean;
}

/**
 * SkillDetails accompanies a call to a RuntimeSkill capability. Mirror of
 * skillruntime.CallResult; populated even when the call fails (only the
 * capability identity is guaranteed then), so a failed call still renders as
 * a skill card with its arguments. The wire name is skill_<sanitized contract
 * ID> — display only, never parsed.
 */
export interface SkillDetails {
  /** The contract ID that ran, e.g. "malware.sample.search". */
  capability?: string;
  callId?: string;
  /** The capability's structured result payload, verbatim. */
  data?: unknown;
  artifacts?: SkillArtifact[];
  packageDigest?: string;
  executorIdentity?: string;
}

/** One produced artifact's metadata; the bytes are fetched per session. */
export interface SkillArtifact {
  artifactId?: string;
  name?: string;
  mediaType?: string;
  digest?: string;
  sizeBytes?: number;
}

export type ToolDetails =
  | ReadDetails
  | ReadManyDetails
  | LsDetails
  | FindDetails
  | GrepDetails
  | WriteDetails
  | EditDetails
  | BashDetails
  | SubagentDetails
  | SubagentListDetails
  | TodoDetails
  | MCPDetails
  | SkillDetails
  | ShortcutDetails
  | SkillAuthoringDetails
  | WorkflowAuthoringDetails
  | AgentWorkflowDetails
  | UserInputDetails
  | BrowserDetails;

export type ShortcutMutationAction = "folder_create" | "update" | "folder_rename" | "move" | "delete" | "merge";
export interface ShortcutChange {
  action: Exclude<ShortcutMutationAction, "merge">;
  kind: "folder" | "shortcut";
  id: string;
  title: string;
  from_parent_id?: string;
  to_parent_id?: string;
}
export interface ShortcutMergePlan {
  id: string;
  revision: number;
  groups: { keep_id: string; duplicate_ids: string[]; title: string; parent_id?: string }[];
  removals: number;
}
/** A host receipt, never generated from model text. A merge plan is only a preview. */
export interface ShortcutDetails {
  type: "shortcut";
  action: "search" | "read" | "list" | "create" | ShortcutMutationAction;
  saved?: boolean;
  changed?: boolean;
  agent_id?: string;
  shortcut?: import("./shortcuts").ShortcutSnapshot;
  folder?: import("./shortcuts").ShortcutFolder & { read_only?: boolean };
  changes?: ShortcutChange[];
  plan?: ShortcutMergePlan;
  revision?: number;
  /** The original committed revision; an idempotent replay may observe a newer library. */
  applied_revision?: number;
  request_id?: string;
  reused?: boolean;
}

/** Private instruction Skill metadata. The host chooses its scope and identity. */
export interface AuthoredSkillSummary {
  id: string;
  name: string;
  description: string;
  revision: string;
  version: number;
  editable: boolean;
  enabled: boolean;
  source: string;
}
export interface AuthoredSkillVersion {
  revision: string;
  version: number;
  created_at: string;
  enabled: boolean;
}
/** Only a successful skill_save host result may show a saved Skill card. */
export interface SkillAuthoringDetails {
  type: "skill_authoring";
  action: "save";
  saved: true;
  agent_id: string;
  skill: AuthoredSkillSummary;
  request_id: string;
  reused: boolean;
  enabled: boolean;
  pending: boolean;
  behavior_tested: false;
}

/** Trusted browser operation receipt. Page identity is display metadata;
 * selecting its Sheet does not grant the page's control lease. */
export interface BrowserDetails {
  action: string;
  ok: boolean;
  backend?: string;
  context_ref?: string;
  active_tab?: string;
  version?: string;
  url?: string;
  title?: string;
  favicon?: string;
  steps?: number;
  content_source?: string;
  has_readable_content?: boolean;
}

/** TodoStatus mirrors the enum in tools/todo.go. */
export type TodoStatus = "pending" | "in_progress" | "completed" | "cancelled" | "blocked";

export interface TodoItem {
  task: string;
  status: TodoStatus;
}

/**
 * The task list as the agent last wrote it.
 *
 * No counts, unlike LsDetails: everything is in the array, so a precomputed total
 * would only be a second thing able to disagree with it.
 *
 * Every one of these on the timeline is a historical write, and only the newest is
 * the current plan — see TimelineCall.superseded.
 */
export interface TodoDetails {
  /** The Go tool serializes a cleared nil slice as null, including in history. */
  todos: TodoItem[] | null;
}

/** A finished delegation, including optional metadata in older transcripts. */
export interface SubagentDetails {
  id: string;
  agent_id?: string;
  run_id?: string;
  status?: string;
  mode: string;
  model?: string;
  /** Directory where a writable child leaves its files, including partial work. */
  workspace?: string;
  /** Historical Git metadata remains readable in saved transcripts. */
  ref?: string;
  commit?: string;
  session?: string;
  worktree?: string;
  turns?: number;
  input_tokens?: number;
  output_tokens?: number;
  exit_code?: number;
  tampered?: boolean;
  /**
   * background marks a delegation that was answered at dispatch time: the real
   * result arrived later through the session's notification channel, and the
   * card's live state comes from bg_tasks rather than from the call itself.
   */
  background?: boolean;
}

export interface RetainedSubagent {
  agent_id: string;
  mode: string;
  model: string;
  status: "queued" | "running" | "idle" | "interrupted" | "archived";
  session?: string;
  run_id?: string;
  last_run_status?: string;
  task?: string;
  result?: string;
  error?: string;
  created_at: number;
  updated_at: number;
}

export interface SubagentListDetails { agents: RetainedSubagent[]; }

/**
 * BgTask is one backgrounded delegation as the server knows it (BgTaskLive in
 * web/wire.go). It lives outside `live` on purpose: a background task outlives
 * every run, and `live` is wiped at each run's end.
 */
export interface BgTask {
  task_id: string;
  mode: string;
  /** The delegated brief, clipped server-side. */
  task: string;
  status: "running" | "done" | "failed";
  started_at: number;
  frames?: SubagentFrame[];
  /** The settled SubagentDetails, present once status leaves "running". */
  details?: SubagentDetails;
  error?: string;
  duration_ms?: number;
}

/**
 * CiteSource is the provenance stamp a tool_end can carry: which label the
 * model's `[1](#cite:S3)` anchors resolve to, what was read or run, and an
 * excerpt of the evidence. Absent on todo and on every call of a
 * `-no-citations` run — citations only exist where a source was stamped.
 */
export interface CiteSource {
	/** Exact immutable library snapshot stamped by the host information tool. */
	information?: { entry_id: string; version: number; snapshot_sha256: string };
  label: string;
  type: string;
  title: string;
  locator: string;
  call_id: string;
  excerpt: string;
  hash: string;
  full_path?: string;
  ts: number;
  /** UI-only original output from the same persisted tool result, never an AI summary. */
  content?: string;
  /** UI-only execution metadata, when supplied by the original result. */
  exit_code?: number;
  error?: boolean;
  /** UI-only website icon recorded with the result; never a remote image URL. */
  favicon?: string;
  /** UI-only: a verified browser operation receipt with no recorded page content. */
  browser_status_only?: boolean;
}

export interface ToolResult {
  call_id: string;
  name?: ToolName;
  text: string;
  is_error?: boolean;
  /** The citation stamp, when the call produced one. See CiteSource. */
  source?: CiteSource;
  // Details never enter the model's context, which is why a full diff can live
  // here while the model only sees the truncated text.
  details?: ToolDetails;
  /**
   * How long the call took, wall-clock ms. Computed server-side from the
   * tool's start/end stamps; absent on transcripts recorded before the field
   * existed and on bash, which has always carried its own duration_ms in
   * details.
   */
  duration_ms?: number;
  /**
   * A delegated run's process frames, retained client-side when a subagent call
   * settles. The server never sends this: unlike live output, whose bytes are
   * the result text, frames are the run's process and the result is its answer,
   * so keeping both past tool_end renders nothing twice. A transcript replayed
   * from disk has none — the live client is the only place they survive.
   */
  frames?: SubagentFrame[];
}

export interface PendingTool {
  call_id: string;
  name: ToolName;
  args?: unknown;
  started_at: number;
  /**
   * What the call has printed so far, for the tools that report it (bash only).
   * Accumulated server-side as well, because tool_partial is not replayed: a page
   * that connects mid-command sees this in the snapshot or sees nothing.
   */
  output?: string;
  /**
   * The structured progress events of a call that reports them — today only a
   * subagent, whose child emits this same event contract one level down. Kept
   * server-side too, for the same reason `output` is.
   */
  frames?: SubagentFrame[];
}

/**
 * The raw arguments of a call the model is still generating. A `write` with a
 * huge `content` streams for minutes before its tool_start, and this is the
 * only sign of life in that gap. `head`/`tail` are windows onto the raw,
 * still JSON-escaped argument text — never parsed, because it is incomplete
 * by definition until tool_start replaces the entry. Both are capped
 * server-side (head 4096, tail 8192) and `bytes` is the authoritative total,
 * so a reconnecting client can rebuild the entry from the snapshot alone.
 */
export interface IncomingArgs {
  call_id: string;
  /** Empty until the first fragment naming the tool arrives. */
  name: string;
  head?: string;
  tail?: string;
  bytes: number;
  /** Content newlines seen so far — the live +N of a write in flight. */
  lines?: number;
  ts?: number;
}

/**
 * One event from a delegated run. It is the same shape as the events of this run,
 * one level down, which is what lets a subagent be rendered without a second
 * vocabulary. Every field is optional because which ones are set depends on `type`.
 */
/**
 * The frame kinds the parent forwards, per frameWorthForwarding in tools/subagent.go.
 *
 * Its own union rather than EventType: a child runs as `pogo -mode json`, so its
 * events come from the CLI contract, which has kinds the browser's stream does not —
 * `session` is one, and typing frames as EventType made it a compile error to
 * describe a real frame.
 *
 * Left open to other strings on purpose. The allowlist is enforced in Go, and if it
 * ever grows, a closed union here would be a lie while the runtime quietly ignores
 * the new kind — which is the behaviour we want.
 */
export type SubagentFrameType =
  | "session"
  | "turn_start"
  | "tool_start"
  | "tool_end"
  | "run_end"
  // The two delta streams, forwarded coalesced: every frame of the kind carries
  // the accumulated tail of the current turn's thinking / answer, never a bare
  // delta, so a consumer joining from the last frame alone can still render.
  | "thinking"
  | "token"
  // eslint-disable-next-line @typescript-eslint/ban-types
  | (string & {});

export interface SubagentFrame {
  type: SubagentFrameType;
  turn?: number;
  session?: string;
  /**
   * The model the child is running, on the session header — the first frame a
   * delegation produces, so a card can say what it is running on from the start
   * rather than only once the settled details repeat it.
   */
  model?: string;
  /**
   * Ties a delegated tool_start to its tool_end. Needed rather than convenient: a
   * child runs calls in parallel, so one turn can hold three greps, and pairing them
   * by name would attribute the results in whatever order they finished.
   */
  call_id?: string;
  name?: ToolName | string;
  /**
   * The call's arguments on tool_start — the parent's wire contract puts them
   * there, and the child is forwarded verbatim, so a row can say which file a
   * read read rather than just "read".
   */
  args?: unknown;
  text?: string;
  is_error?: boolean;
  /**
   * The call's structured payload on tool_end (ReadDetails and friends), the
   * same shape the parent's own tool results carry: the child speaks this same
   * contract one level down, so the result components render it unchanged.
   */
  details?: ToolDetails;
  error?: string;
  stop_reason?: string;
}

export interface PendingGate {
  gate_id: string;
  call_id: string;
  tool: ToolName;
  args?: unknown;
  // Absolute epoch milliseconds. Never a duration: a reloaded page has to be
  // able to recompute the time left.
  deadline: number;
  frozen?: boolean;
  remaining_ms?: number;
  danger?: string[];
  /** Which subagent asked, empty for the session's own calls. Such a gate
   * survives a run's end, because a background task asks whenever it asks. */
  origin?: string;
}

export interface UserInputOption {
  label: string;
  description: string;
}

export interface UserInputQuestion {
  id: string;
  header: string;
  question: string;
  options: UserInputOption[];
  multi_select: boolean;
}

export interface PendingUserInput {
  request_id: string;
  call_id: string;
  run_id?: string;
  revision: number;
  questions: UserInputQuestion[];
  deadline: number;
  async?: boolean;
}

export interface UserInputResolution {
  request_id: string;
  call_id?: string;
  status: "answered" | "cancelled" | "timeout";
  reason?: string;
  async?: boolean;
  answers?: Record<string, string[]>;
}

export interface UserInputDetails {
  request_id?: string;
  call_id?: string;
  status?: string;
  async?: boolean;
  questions: UserInputQuestion[];
  answers?: Record<string, string[]>;
}

export interface Live {
  run_id?: string;
  active: boolean;
  turn?: number;
  message_id?: string;
  thinking?: string;
  text?: string;
  pending_tools: PendingTool[];
  pending_gates: PendingGate[];
  pending_user_inputs: PendingUserInput[];
  /**
   * Calls whose arguments are still streaming in, oldest first. Every entry
   * precedes its tool_start, which removes it: the pending-tool card takes
   * over from there.
   */
  incoming?: IncomingArgs[];
}

export interface RunInfo {
  active: boolean;
  run_id?: string;
  model?: string;
  provider?: string;
  /** Latest terminal status retained by the live session Hub. */
  end_reason?: string;
  error?: string;
  /** From the model catalog; absent for a model that is not in it. */
  context_window?: number;
  /** Saved model settings will apply after the current run finishes. */
  model_config_pending?: boolean;
}

export type PolicyMode = "strict" | "standard" | "auto";

export interface PolicyState {
  mode: PolicyMode;
  remaining_turns?: number;
}

export interface Usage {
  input: number;
  output: number;
  cache_read?: number;
  reasoning?: number;
}

/**
 * One itemized share of the fixed request cost (agent.OverheadPart). `name`
 * is a stable prompt-section key for kind "prompt", a tool name for "tool",
 * a Skill name for "skill_tools" and an MCP server for "mcp".
 */
export interface OverheadPart {
  kind: "prompt" | "tool" | "skill_tools" | "mcp";
  name: string;
  tokens: number;
  /** Declarations in a grouped part. */
  count?: number;
}

/** Host-authenticated role of a schedule execution, separate from ordinary chats. */
export interface BackgroundTaskSessionIdentity {
  source: "schedule";
  task_id: string;
  source_id: string;
  agent_id: string;
  run_id?: string;
}

export interface Snapshot {
  seq: number;
  /** Replay instance; sequence numbers are comparable only within this value. */
  stream_instance?: string;
  messages: Message[];
  results: Record<string, ToolResult>;
  user_input_answers?: Record<string, UserInputDetails>;
  live: Live;
  run: RunInfo;
  policy: PolicyState;
  /** Running total, for cost. */
  usage: Usage;
  /** Backgrounded delegations, running and recently finished alike. */
  bg_tasks?: BgTask[];
  /** Session command jobs survive run_end and are restored on reconnect. */
  shell_tasks?: ShellJobRecord[];
  /** Durable follow-ups, including those not yet delivered to the model. */
  inbox?: InboxItem[];
  /**
   * The latest turn's prompt size, for occupancy. Not derivable from `usage`:
   * that accumulates across turns and would read many times the window.
   */
  context_tokens: number;
  /** The retained count uses a previous model's tokenizer and is approximate. */
  context_estimated?: boolean;
  /**
   * Estimated fixed request cost (system prompt + tool schemas), for the
   * context meter's breakdown. An estimate, never a measurement.
   */
  overhead_tokens?: number;
  /** What overhead_tokens is made of, in the same units. */
  overhead_breakdown?: OverheadPart[];
  /**
   * Prompt size at which the server starts dropping old tool results; absent or 0
   * when clearing is off. The gauge colours its bands against this rather than
   * against fractions of the window, because clearing holds occupancy just below it.
   */
  clear_trigger?: number;
  /**
   * The latest generated follow-up chips and the assistant message they belong
   * to, so a client that missed the event (reconnect, fresh load) can offer
   * them without waiting for another turn. Absent when none were generated.
   */
  followups?: { message_id?: string; chips: StarterCard[] };
  answer_review?: AnswerReview;
}

export type EventType =
  | "snapshot"
  | "inbox_updated"
  | "run_start"
  | "user_message"
  | "turn_start"
  | "thinking"
  | "answer_review"
  | "token"
  | "message"
  | "tool_args"
  | "tool_start"
  | "tool_partial"
  | "tool_end"
  | "retry"
  | "run_end"
  | "followups"
  | "error"
  | "gate_request"
  | "gate_deadline"
  | "gate_resolved"
  | "gate_auto"
  | "user_input_request"
  | "user_input_resolved"
  | "policy_changed"
  | "policy_reverted"
  | "model_changed"
  | "model_config_pending"
  | "tool_config"
  | "rewound"
  | "compacted"
  | "subagent_start"
  | "subagent_frame"
  | "subagent_end"
  | "shell_task_start"
  | "shell_task_output"
  | "shell_task_end"
  | "attachment_uploaded"
  | "browser_downloads"
  | "shortcuts_changed"
  | "image_describe_start"
  | "image_describe_end"
  | "office_open"
  | "office_sync";
// Office opening is a server-authenticated UI reveal, separate from tool text.

export interface AgentEvent {
  seq: number;
  type: EventType;
  ts: number;
  answer_review?: AnswerReview;
  document_session_id?: string;
  conversation_id?: string;
  path?: string;
  file_type?: "docx" | "xlsx";
  shortcut_agent_id?: string;
  tool_config?: import("@/agent/studioSessionCapabilities").StudioToolConfigSync;
  shortcut_revision?: number;
  shortcut_id?: string;

  run_id?: string;
  model?: string;
  provider?: string;
  context_window?: number;
  model_config_pending?: boolean;
  turn?: number;
  clear_trigger?: number;
  /** On model_changed, and on turn_start when the tool view changed it. */
  overhead_tokens?: number;
  overhead_breakdown?: OverheadPart[];

  message_id?: string;
  role?: "user" | "assistant";
  text?: string;
  content?: Block[];
  /**
   * On `user_message`: the client-generated id the POST carried, echoed so the
   * sender can retire the optimistic bubble it drew before the round-trip.
   * Never persisted — it reconciles a live fold, nothing more.
   */
  client_id?: string;
  inbox_id?: string;
  steering?: boolean;
  input_layout?: InputLayout;
  item?: InboxItem;

  call_id?: string;
  name?: ToolName;
  args?: unknown;
  is_error?: boolean;
  details?: ToolDetails;
  /** The citation stamp, on `tool_end`; absent for todo and `-no-citations`. */
  source?: CiteSource;
  /** How long the call took, on `tool_end`; the hub computes it. */
  duration_ms?: number;
  /** One event from a delegated run, on `tool_partial`. See SubagentFrame. */
  frame?: SubagentFrame;
  /** The background task a `subagent_*` event belongs to. */
  task_id?: string;
  /** Complete, revisioned record on shell_task_* events. */
  shell_task?: ShellJobRecord;
  /**
   * On `subagent_end`, the task's capped frame list: the only copy a client
   * that missed frames live can ever get, since frames are not replayed. See
   * web/wire.go.
   */
  frames?: SubagentFrame[];

  gate_id?: string;
  /** Background subagent identity; its approval can outlive the parent run. */
  origin?: string;
  deadline?: number;
  frozen?: boolean;
  remaining_ms?: number;
  danger?: string[];
  allow?: boolean;
  reason?: string;
  by?: string;
  rule?: string;
  user_input?: PendingUserInput;
  user_input_resolution?: UserInputResolution;

  policy?: PolicyState;
  from?: string;
  to?: string;

  attempt?: number;
  max?: number;
  delay_ms?: number;

  stop_reason?: string;
  usage?: Usage;
  /** Harness reason for run termination; more precise than the model stop reason. */
  end_reason?: string;
  error?: string;
  // Steering messages the run accepted but never passed to the model.
  undelivered?: string[];

  /**
   * On `followups`: the next-step chips the server generated for the turn of
   * `message_id`, sent once the turn has ended. The client offers them in place
   * of the skills' rule-matched chips, which stay the fallback.
   */
  chips?: StarterCard[];

  snapshot?: Snapshot;

  /**
   * On `attachment_uploaded`: the saved upload's metadata, in the same shape
   * the upload endpoint returned — a pane files it without a second request.
   * Logged server-side, so a resumed stream replays it; a fresh snapshot does
   * not, which is what `sessionAttachments` (the GET listing) seeds.
   */
  attachment?: AttachmentMeta;
  browser_downloads?: import("@/agent/browserDownloads").BrowserDownloadSnapshot;

  /**
   * On `image_describe_start` / `image_describe_end`: the image the vision
   * helper is describing for a text-only conversation model. image_ref ties
   * a start to its end (one run never describes the same ref twice at once);
   * the end carries the description in `text`, or the failure in `error`,
   * and the step's duration in `duration_ms`.
   */
  image_ref?: string;
  image_name?: string;
}

export interface SessionInfo {
  path: string;
  id: string;
  title: string;
  /** Pinned sessions sort above the rest; set from the sidebar's ⋮ menu. */
  pinned?: boolean;
  /** A run is in flight right now — the sidebar's working indicator reads this. */
  busy?: boolean;
  cwd?: string;
  model?: string;
  messages: number;
  created?: number;
  updated?: number;
}

/** One quoted stretch of conversation around a cluster of matches: hits close
 * enough to share one display window merge into a single block, distant ones
 * quote separately. anchor is the same window raw, verbatim from the message,
 * so the UI can locate the message holding it with a substring search. */
export interface SearchBlock {
  text: string;
  anchor: string;
}

/** A full-text search hit: the session row plus how well it matched. bm25
 * scores are negative with the best match nearest zero. blocks quote the
 * conversation around the matches; absent when only the title matched. */
export interface SessionHit extends SessionInfo {
  score: number;
  blocks?: SearchBlock[];
}

export interface SkillInfo {
  aliases?: string[];
  name: string;
  description: string;
  // The absolute path to SKILL.md. It is already in the model's system prompt, so
  // showing it here keeps the user from knowing less than the model does.
  path: string;
  dir: string;
  source: string;
  manual_only?: boolean;
  // The manifest tools this skill's tools.json declares, in file order. Absent
  // for skills without a manifest — most of them.
  tools?: ToolCatalogEntry[];
}

// ToolCatalogEntry is one declared tool: the MCP Tool object shape plus the
// pogo extensions. Two provenances share the shape — a skill's tools.json
// (the loader vetted it; `registered` says whether the model can call it
// directly or it is display-only) and a live MCP server's tools/list (those
// carry `server`, and no renderer/registered: a remote declaration is always
// callable through its connection and never has a pogo renderer).
export interface ToolCatalogEntry {
  name: string;
  wire_name: string;
  title?: string;
  description: string;
  input_schema: {
    type: string;
    properties?: Record<string, { type?: string; description?: string; enum?: string[] }>;
    required?: string[];
  };
  read_only?: boolean;
  destructive?: boolean;
  idempotent?: boolean;
  open_world?: boolean;
  renderer?: string;
  skill?: string;
  registered?: boolean;
  server?: string;
  group?: string;
}

// McpServerGroup is one MCP server's panel card: the pool's live status
// beside its tools — the cached catalog when the server is down, so an
// outage greys the group instead of vanishing it.
export interface McpServerGroup {
  name: string;
  /** connecting | connected | disconnected */
  state: string;
  server_name?: string;
  server_version?: string;
  last_error?: string;
  catalog_stale?: boolean;
  tools: ToolCatalogEntry[];
}

// BuiltinToolEntry is one native tool in /api/tools' builtin section: the
// fixed platform verbs. `protected` means an Agent's tool selection cannot
// withhold it (the read four, todo, subagent, analyze_image); the rest —
// write, edit, bash, browser — are the editor's actual checkboxes.
export interface BuiltinToolEntry {
  name: string;
  description: string;
  read_only?: boolean;
  protected?: boolean;
  skill_binding?: string;
}

// Read-only text from a loader-authorized Skill package. Optional metadata
// keeps root-document previews compatible with older servers.
export interface SkillDocumentContent {
  content: string;
  truncated: boolean;
  relative_path?: string;
  language?: string;
  media_type?: string;
  /** Only the administrator's ordinary uploaded SKILL.md may be edited. */
  editable?: boolean;
  revision?: string;
  edit_disabled_reason?: string;
  /** Authored Skills use the complete bundle revision for concurrent saves. */
  version?: number;
}

export interface SkillDocumentUpdate { content: string; expected_revision: string }

export interface SkillContent extends SkillDocumentContent {
  name: string;
  path: string;
  source: string;
}

export interface SkillPackageEntry {
  name: string;
  path: string;
  kind: "file" | "directory" | "symlink" | "other";
  size_bytes: number;
  previewable: boolean;
  expandable?: boolean;
  language?: string;
  media_type?: string;
  reason?: string;
}
export interface SkillPackageFiles {
  relative_path: string;
  entries: SkillPackageEntry[];
  truncated: boolean;
  limits: { max_entries: number; max_depth: number };
}

// ── Skill upload / session import-export / model settings ──────────────────

// One installed version of the same package that an incoming upload would
// replace — the confirm step shows these beside the incoming version.
export interface SkillUploadExistingVersion {
  skill_id: string;
  version: string;
  installed_at?: string;
  revoked?: boolean;
}

// The install result for one uploaded zip: the name the loader resolved and
// where the bundle landed. Warnings are the loader's diagnostics (collisions,
// oversize bodies) — informational, the install itself succeeded. A runtime
// package whose id is already installed at another version answers with
// status "confirm_replace" instead: nothing is installed until the user
// resolves the parked upload (replace / keep-old).
export interface SkillUploadResult {
  status?: 'installed' | 'confirm_replace' | 'kept_old';
  confirm_token?: string;
  incoming?: { id: string; version: string; name?: string };
  existing?: SkillUploadExistingVersion[];
  replaced_versions?: string[];
  name: string;
  path?: string;
  skill_ids?: string[];
  version?: string;
  warnings?: string[];
  /** Runtime-package readiness is supplied by the server after installation. */
  ready?: boolean;
  execution?: import('./skillTypes').SkillExecutionStatus;
  issues?: import('./skillTypes').SkillExecutionIssue[];
}

// The outcome of an import: what landed (with the ids the sidebar will show)
// and per-entry reasons for anything skipped.
export interface SessionImportResult {
  imported: number;
  session_ids: { id: string; title: string }[];
  skipped?: string[];
}

// One shared answer, as the creating dialog and the public /share page both
// read it. A snapshot, not a view: the conversation can continue or disappear
// and this stays what it was when the link was made.
export interface ShareEntry {
  id: string;
  title?: string;
  model?: string;
  created_at: number;
  session_id?: string;
  question?: string;
  answer: string;
  // The exchange's timeline projection — the same messages+results shape the
  // session view renders from, so the share page can show the work log, not
  // just the bare answer. Absent on shares created before snapshots carried
  // it; the page falls back to the question/answer layout for those.
  messages?: Message[];
  results?: Record<string, ToolResult>;
}

// One provider as the settings dialog sees it: the file's fields plus whether
// the key variable currently resolves server-side. key_value carries the
// resolved value when one exists — for the curl preview's real-request
// reproduction — under the same authenticated trust bar as the rest of /api.
export interface ConfigProviderInfo {
  name: string;
  base_url: string;
  key_env: string;
  base_url_env?: string;
  headers?: Record<string, string>;
  extra_body?: Record<string, unknown>;
  allow_insecure_http?: boolean;
  configured: boolean;
  key_value?: string;
}

// The providers.json shapes the dialog edits. Mirrors the server's strict
// decoder: unknown fields there are refused, so this side models exactly the
// known ones.
export interface ProviderFileEntry {
  base_url: string;
  key_env: string;
  base_url_env?: string;
  /** Extra request headers — a vendor gateway's proprietary switch. */
  headers?: Record<string, string>;
  /** Merged over the request body's top level — the body-side sibling of headers. */
  extra_body?: Record<string, unknown>;
  /** Explicit opt-in for plain http to a host that is neither https nor private. */
  allow_insecure_http?: boolean;
}

export interface ModelFileEntry {
  compatibility?: {
    tool_calls?: "native" | "xml" | "json";
    tool_history?: "native" | "text";
    preserve_reasoning?: boolean;
    allow_mcp_aliases?: boolean;
    allow_text_preamble?: boolean;
  };
  id: string;
  provider: string;
  aliases?: string[];
  context_window?: number;
  max_tokens?: number;
  subagent_model?: string;
  followup_model?: string;
  subagent?: boolean;
  /** Tri-state: absent means judge by the id, true/false override the guess. */
  vision?: boolean;
}

export interface ConfigFile {
  default?: string;
  /** The helper model that sees images on behalf of text-only ones. */
  vision_model?: string;
  providers?: Record<string, ProviderFileEntry>;
  models?: ModelFileEntry[];
}

export interface ConfigInfo {
  path: string;
  raw: string;
  file: ConfigFile | null;
  parse_error?: string;
  providers: ConfigProviderInfo[];
  default?: string;
}

export interface ConfigSaveResult {
  ok: boolean;
  warnings?: string[];
  default?: string;
  models?: number;
}

// What came back from a probe: ok=false with error covers transport failures
// (DNS, refused, timeout); ok=true wraps the gateway's own answer, truncated
// when a live stream hit the 64 KiB / deadline read cap.
export interface ConfigProbeResult {
  ok: boolean;
  status?: number;
  status_text?: string;
  content_type?: string;
  elapsed_ms?: number;
  body?: string;
  truncated?: boolean;
  error?: string;
}

/** Read-only metadata queries. These never save provider or model settings. */
export interface ConfigMetadataProbeRequest {
  name: string;
  base_url: string;
  key_env: string;
  key: string;
  model: string;
  headers: Record<string, string>;
  preset: "vllm" | "ollama-running" | "ollama-show" | "llamacpp";
}

// One graded question of the vision probe: the ask (options included), the
// letter that is true of the pixels, and what the model actually replied.
export interface VisionProbeQuestion {
  ask: string;
  options: string[];
  answer: string;
  reply: string;
  pass: boolean;
}

// The verdict on "does this model see images". "yes" only when every question
// was answered right; "no" when the replies missed; rejected means the
// endpoint refused image content outright; ok=false with error leaves the
// question open (bad key, timeout, non-image HTTP error).
export interface VisionProbeResult {
  ok: boolean;
  verdict?: "yes" | "no";
  rejected?: boolean;
  note?: string;
  elapsed_ms?: number;
  questions: VisionProbeQuestion[];
  error?: string;
}

// The probe's raw material, fetched for the curl preview: each question's ask
// and the inline data URL of its image, exactly as the request will carry it.
export interface VisionProbePreview {
  questions: { ask: string; image_url: string }[];
}

// ── Storage settings (prefs.json + the spill directory) ─────────────────────

// Prefs mirrors prefs.Prefs: the settings dialog's non-provider settings.
export interface Prefs {
  /** Days a spill file is kept; 0 disables automatic cleanup entirely. */
  spill_retention_days: number;
  /**
   * Runtime override of the interactive turn cap; 0 means the -max-turns
   * flag decides. Edited through PUT /api/max-turns, not the wholesale
   * prefs save, so this field survives every other sheet's save.
   */
  max_turns?: number;
  /**
   * Automatic memory distillation for interactive Agent sessions. Edited
   * through PUT /api/memory-auto; sessions read the live value at a run's
   * end, so the switch applies without a reload.
   */
  memory_auto?: boolean;
}

// MaxTurnsInfo is GET /api/prefs's turns block and PUT /api/max-turns's
// answer: the override (0 = none), the flag it falls back to, and the cap
// runs use right now.
export interface MaxTurnsInfo {
  max_turns: number;
  flag: number;
  effective: number;
}

// SpillUsage is the spill directory's current cost — the number behind the
// dialog's "how much disk is this" line.
export interface SpillUsage {
  files: number;
  bytes: number;
  dir: string;
}

// SweepInfo is one completed sweep, automatic or the dialog's prune-now.
export interface SweepInfo {
  time: string;
  removed: number;
  bytes: number;
}

// PrefsInfo is GET /api/prefs. load_error is set when the prefs file cannot
// be read — the automatic sweep is paused then, and the section renders with
// the defaults anyway so the file can be fixed from the dialog.
export interface PrefsInfo {
  prefs: Prefs;
  spill: SpillUsage;
  turns: MaxTurnsInfo;
  last_sweep?: SweepInfo | null;
  load_error?: string;
}

// SpillPruneResult is POST /api/prefs/prune-spill's answer.
export interface SpillPruneResult {
  removed: number;
  bytes: number;
}

// Administrator-only, read-only browsing of the configured Spill directory.
// Paths are root-relative selectors, never arbitrary host file paths.
export interface SpillEntry {
  name: string;
  path: string;
  type: "directory" | "file";
  size: number;
  modified_at: string;
}

export interface SpillEntries {
  root: string;
  path: string;
  parent: string;
  read_only: true;
  exists: boolean;
  entries: SpillEntry[];
  offset: number;
  limit: number;
  next_offset: number | null;
  complete: boolean;
  truncated: boolean;
  skipped: number;
  scan_limit_reached: boolean;
}

export interface SpillFile {
  root: string;
  path: string;
  name: string;
  read_only: true;
  size: number;
  modified_at: string;
  offset: number;
  bytes: number;
  next_offset: number | null;
  complete: boolean;
  truncated: boolean;
  binary: boolean;
  encoding: "utf-8";
  content: string;
}

// SandboxInfo is GET/PUT /api/sandbox's payload: the full containment state,
// not just the current setting — the dialog renders the consequences from it.
export interface SandboxInfo {
  /** The deployment's baseline (POGO_SANDBOX, degrade by default). */
  env_mode: "enforce" | "degrade" | "off";
  /** The runtime override, "" when none (the env decides). */
  override: "" | "enforce" | "degrade" | "off";
  /** What commands actually run under right now. */
  effective: "enforce" | "degrade" | "off";
  /** Whether commands really run confined at this moment. */
  active: boolean;
  /** Why the sandbox cannot be used (empty when it can). */
  unavailable?: string;
  /** Whether the probe got a private /proc. */
  proc: boolean;
  /** Whether the sandbox allows network (POGO_SANDBOX_NET=1). */
  net: boolean;
}

// AttachmentPolicy is GET/PUT /api/attachments/policy's payload: the one
// allowlist the composer's door check, the picker's accept attribute and the
// server's saveAttachment gate all read, so the three cannot drift apart.
export interface AttachGroup {
  id: string;
  exts: string[];
}

export interface AttachmentPolicy {
  groups: AttachGroup[];
  default_exts: string[];
  /** The saved override; absent means the deployment default applies. */
  override?: string[];
  /** The effective list, sorted, dot-prefixed. */
  allowed_exts: string[];
  /** The effective per-file cap in MB (override resolved against the default). */
  max_mb: number;
  /** The saved cap override; absent/0 means the built-in default applies. */
  max_mb_override?: number;
  min_mb: number;
  cap_mb: number;
}

// ── MCP client settings (mcp.json) ──────────────────────────────────────────

// MCPServerConfig mirrors mcpclient/config.go's ServerConfig: one external MCP
// server — a command to run (stdio) or a URL to reach (streamable HTTP / the
// legacy SSE transport). Everything is optional in the file: enabled defaults
// to true, transport to "streamable", and the timeouts are defaulted and
// clamped server-side. The file never holds a credential — bearer_token_env
// names the environment variable, and env/header values may reference $VARs.
export interface MCPServerConfig {
  enabled?: boolean;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
  url?: string;
  transport?: "streamable" | "sse";
  headers?: Record<string, string>;
  bearer_token_env?: string;
  /** The literal token, mutually exclusive with bearer_token_env: it lands in
      mcp.json (0600) in plaintext, so the variable form stays the
      recommendation. A value starting with '$' or pure whitespace is
      rejected server-side. */
  bearer_token?: string;
  startup_timeout_ms?: number;
  tool_timeout_ms?: number;
  include_tools?: string[];
  exclude_tools?: string[];
  trust?: boolean;
  required?: boolean;
}

export interface MCPConfig {
  servers?: Record<string, MCPServerConfig>;
  /** The second section: this machine *as* an MCP server (the other
      direction). See MCPServeConfig. */
  server?: MCPServeConfig;
}

// MCPServeConfig mirrors the "server" section of mcp.json: how this agent
// serves others over /mcp and /sse. token and token_env are mutually
// exclusive; neither means no auth at all (a Bearer header is accepted but
// not required). enabled/token/policy are hot-applied on save; the quota
// fields need a restart (server_state.restart_required says when they have
// drifted from the running values).
export interface MCPServeConfig {
  enabled?: boolean;
  token_env?: string;
  token?: string;
  policy?: "" | "strict" | "standard" | "auto";
  /** Tool allowlist: plain names or a single trailing-* prefix (mal.*).
      Absent or empty exposes every builtin + skill tool — never forwards or
      runtime skill packages, which are exposed only when explicitly
      matched. Hot-applied on save. */
  include_tools?: string[];
  max_concurrent?: number;
  submit_rpm?: number;
  max_waits?: number;
}

// MCPServerState is GET /api/mcp's server_state: the *effective* runtime
// state of the serving half. source says where the effective config comes
// from — "env" means the POGO_MCP* variables have taken over and editing the
// file changes nothing (the form disables itself then).
export interface MCPServerState {
  enabled: boolean;
  token_required: boolean;
  policy: string;
  source: "env" | "file" | "default";
  restart_required: boolean;
  endpoints: string[];
  /** The effective tool allowlist (absent/empty = every builtin + skill
      tool exposed, forwards and runtime packages only when explicitly
      matched). */
  include_tools?: string[];
}

// MCPServeCatalog is GET /api/mcp's serve_catalog: every tool this machine
// could expose as an MCP server, grouped for the serve sheet's check tree
// (components/mcpServePicker.ts). Absent on older backends — the sheet falls
// back to the comma box then.
export interface MCPServeCatalogTool {
  name: string;
  title: string;
}

export interface MCPServeCatalogSkill extends MCPServeCatalogTool {
  /** Only registered skills are real tools; the picker drops the rest. */
  registered: boolean;
}

export interface MCPServeCatalogForward {
  /** The exposed name the allowlist matches: "<server>.<tool>"
      (filesystem.read_file). */
  name: string;
  /** The tool's name on the remote server. */
  remote: string;
  title: string;
  description: string;
  read_only: boolean;
}

export interface MCPServeCatalogServer {
  name: string;
  /** The pool's live state ("connected" …); anything else greys the group
      out in the picker. */
  state: string;
  tools: MCPServeCatalogForward[];
}

// MCPServeCatalogRuntime is one installed runtime skill package (a packed
// skill bundle). Its tools are exposed under skill_<package>_* names and,
// like forwards, are opt-in: an empty include_tools does NOT expose them.
export interface MCPServeCatalogRuntimeTool {
  /** The exposed name the allowlist matches ("skill_malware_sample_search"). */
  name: string;
  /** The original contract id ("malware.sample.search"). */
  contract: string;
  title?: string;
  description?: string;
  read_only?: boolean;
  /** The tool needs sample files pre-uploaded via the builtin
      skill_upload_file. */
  file_inputs?: boolean;
  /** Output artifacts are fetched back via the builtin skill_read_artifact. */
  writes_artifacts?: boolean;
}

export interface MCPServeCatalogRuntime {
  /** "rs_<digest>" — the installed package's catalog identity. */
  id: string;
  name: string;
  package_name: string;
  version: string;
  /** The package's lifecycle ("available" | "revoked" | "unavailable" |
      "needs_configuration"); anything but "available" greys the group out. */
  state: string;
  tools: MCPServeCatalogRuntimeTool[];
}

export interface MCPServeCatalog {
  builtin: MCPServeCatalogTool[];
  skills: MCPServeCatalogSkill[];
  servers: MCPServeCatalogServer[];
  /** Installed runtime skill packages. Absent on older backends — the
      picker's runtime section then does not render at all. When present,
      builtin also carries the skill_upload_file / skill_read_artifact
      helper rows. */
  runtime?: MCPServeCatalogRuntime[];
}

// MCPServerStatus is the connection pool's live view of one server
// (mcpclient.Status). A disabled server keeps no connection, so it never
// appears in this list; last_error is cleared on a successful connect.
export interface MCPServerStatus {
  name: string;
  state: "connecting" | "connected" | "disconnected";
  tools: number;
  last_error?: string;
  protocol_version?: string;
  server_name?: string;
  server_version?: string;
  instructions?: string;
  since: string;
}

// MCPInfo is GET /api/mcp's payload (and PUT's answer): the file's parsed
// content beside the pool's per-server status. load_error is set when
// mcp.json cannot be read — the editor still renders with the empty config,
// because a broken file is exactly when the editor is needed. server_state is
// the serving half's effective runtime state (absent on older backends).
export interface MCPInfo {
  path: string;
  config: MCPConfig;
  load_error?: string;
  status: MCPServerStatus[];
  server_state?: MCPServerState;
  /** The exposable-tool catalog for the serve sheet's picker (absent on
      older backends). */
  serve_catalog?: MCPServeCatalog;
}

// MCPProbeResult is POST /api/mcp/probe's answer: a trial connect of one
// entry, never joining the pool. A connect failure is ok:false carrying the
// reason, not an HTTP error. tools are the server-side names that passed the
// entry's include/exclude filter.
export interface MCPProbeResult {
  ok: boolean;
  tools?: string[];
  /**
   * Tools that pass the filter but would not survive the pool's wrapper
   * validation, with the reason — the same skip the pool otherwise only
   * logs. Absent when everything reported also registers.
   */
  unregistrable?: { name: string; reason: string }[];
  server_name?: string;
  server_version?: string;
  protocol_version?: string;
  error?: string;
}

// StarterCard is one card on the empty conversation. The content comes from a
// skill's starters.json, so the deployment decides what its agent is for; the
// server has already validated that exactly one action is present, that the icon
// is a known name, and that a panel card names a registered panel.
export interface StarterCard {
  icon?: string;
  title: string;
  label?: string;
  /** Put this text in the composer. Mutually exclusive with panel. */
  prompt?: string;
  parts?: MessageInputPart[];
  input_layout?: InputLayout;
  /** Open this dock panel. */
  panel?: string;
  /** Hash route inside the panel, e.g. "#/clusters". */
  at?: string;
}

// FollowupGroup offers the next step after a turn. `when` is matched against
// what the last turn did (its tool calls and reply); no match shows nothing.
export interface FollowupGroup {
  when: string[];
  chips: StarterCard[];
}

export interface Starters {
  /** CAS of the effective homepage source, including deployment defaults. */
  revision?: string;
  heading?: string;
  /** Send a prompt card on click instead of filling the composer. */
  send?: boolean;
  cards: StarterCard[];
  followups?: FollowupGroup[];
  /** True when a user-edited set (POGO_STARTERS_FILE) overrides the skill's. */
  custom?: boolean;
}

// PanelInfo is an external web app registered with -web-panel. path is the
// same-origin prefix the iframe loads (/panels/<name>/); the backend URL is
// never exposed to the page.
export interface PanelInfo {
  name: string;
  path: string;
  entry?: string;
  proxy_mode?: "compat" | "passthrough";
}

export interface ModelInfo {
  id: string;
  provider: string;
  aliases?: string[];
  context_window: number;
  configured: boolean;
  /**
   * The model a read-only subagent of this one runs, from ~/.pogo/providers.json.
   * Worth showing because it is the one catalogue entry that changes what happens
   * without anyone naming it at a prompt.
   */
  subagent_model?: string;
  /**
   * Which environment variable to set, sent only when `configured` is false. The
   * terminal prints its own hint; a browser cannot, so the server names it.
   */
  key_env?: string;
}

export const emptyLive = (): Live => ({
  active: false,
  pending_tools: [],
  pending_gates: [],
  pending_user_inputs: [],
});

// --- Workspace file panel (web-ui-design.md §16) ---------------------------

export interface FileEntry {
  name: string;
  dir: boolean;
  size: number;
  mtime_ms: number;
}

export interface FilesResponse {
  path: string;
  entries: FileEntry[];
  truncated: boolean;
}

// FileContent has two shapes: binary files report {binary, mime, size} only;
// text files carry {text, truncated, ...}. The presence of `binary` is the
// discriminant.
export interface FileContent {
  path: string;
  binary?: boolean;
  mime?: string;
  text?: string;
  truncated?: boolean;
  truncated_by?: "lines" | "bytes" | "";
  size: number;
  mtime_ms?: number;
}

// FileRender is the server's docx/xlsx → markdown conversion for the preview
// tab — the two attachment kinds a browser cannot show on its own.
export interface FileRender {
  path: string;
  text: string;
  truncated: boolean;
}

// A short-lived, read-only capability for a workspace HTML entry and its
// relative project assets. These URLs carry no Pogo account/session token.
export interface WorkspaceHTMLPreview {
  id: string;
  url: string;
  browser_url: string;
  expires_at: string;
  read_only: true;
  path: string;
  project_root: string;
}

// --- Workspace-level changes (journal-backed, web-ui-design.md §16 M4) -----

export interface WorkspaceChange {
  path: string;
  status: "added" | "modified" | "deleted";
  added: number;
  removed: number;
  first_ms: number;
  last_ms: number;
  sid: string;
  base_available: boolean;
}

export interface WorkspaceDiff {
  path: string;
  status: string;
  added: number;
  removed: number;
  base_available: boolean;
  /** too_big means the patch exists on disk but was withheld from the UI. */
  too_big?: boolean;
  patch?: string;
}

export interface FileIndexResponse {
  paths: string[];
  capped: boolean;
}

export interface SessionFileLinkCandidate {
  path: string;
  relative_path: string;
  name: string;
}

export interface SessionFileLinkResolution {
  status: "exact" | "unique" | "ambiguous" | "not_found" | "incomplete";
  candidates: SessionFileLinkCandidate[];
  complete: boolean;
}

// ── Scheduled jobs (the clock panel, web/scheduleapi.go) ────────────────────

// ScheduleJob is one row of the panel: the stored job plus next_run (null
// when the server cannot compute one — disabled, or an every-job that never
// ran) and running (a run-now from this server is in flight).
export interface ScheduleJob {
  id: string; title?: string; revision?: number; prompt: string; workdir: string;
  agent_id?: string; agent_name?: string; agent_model?: string; workflow_id?: string;
  parts?: MessageInputPart[]; input_layout?: InputLayout; source_session_id?: string;
  kind: "cron" | "every" | "at"; cron?: string; every_sec?: number; at?: string;
  timezone?: string; effective_timezone?: string; effective_timezone_label?: string;
  max_turns?: number; budget_usd?: number; model?: string; expires_at?: string;
  /** Missing fields preserve a previously saved task's bounded/standard policy. */
  turn_limit_mode?: "unlimited" | "limited" | "legacy";
  approval_mode?: PolicyMode;
  enabled: boolean; created_at: string; last_run_at?: string; last_run_status?: "" | "ok" | "error" | "skipped";
  last_run_duration_sec?: number; consecutive_failures?: number; session_id?: string;
  last_error?: string; last_summary?: string; result_url?: string; availability_error?: string;
  running_session_id?: string; running_result_url?: string; waiting_human?: boolean;
  next_run: string | null; running?: boolean;
}
export interface ScheduleJobsInfo {
  dir: string; backend: string; jobs: ScheduleJob[];
  server_timezone?: string; server_timezone_label?: string; current_time?: string;
}
export interface ScheduleJobCreate {
  agent_id?: string; title?: string; prompt: string; parts?: MessageInputPart[];
  input_layout?: InputLayout; source_session_id?: string; timezone?: string;
  /** Legacy operator fields, never sent for Agent tasks. */
  workdir?: string; name?: string;
  kind: "cron" | "every" | "at"; cron?: string; every_sec?: number; at?: string;
  max_turns?: number; budget_usd?: number; expires_at?: string;
  turn_limit_mode?: "unlimited" | "limited" | "legacy";
  approval_mode?: PolicyMode;
}
export interface ScheduleJobUpdate extends ScheduleJobCreate { revision: number }
export interface ScheduleRunRecord {
  ts: string; trigger: "scheduled" | "manual"; session_id?: string;
  status: "ok" | "error" | "skipped"; duration_sec: number;
  end_reason?: string; error?: string; summary?: string; model?: string;
  agent_revision?: number | string; result_url?: string;
}
export interface ScheduleRunsInfo { runs: ScheduleRunRecord[]; log_tail: string[]; log_path: string }

export type MCPLogOutcome = "ok" | "error" | "unknown";

// MCPCallLog is one recorded MCP endpoint hit (GET /api/mcplog). diag_code
// maps to problem/fix copy in the mcpLogs i18n namespace; the server never
// sends localized text.
export interface MCPCallLog {
  id?: number;
  ts: number;
  req_id: string;
  transport: "streamable" | "sse";
  http_method: string;
  jsonrpc_method?: string;
  tool?: string;
  session_id?: string;
  protocol_version?: string;
  client_name?: string;
  status: number;
  duration_ms: number;
  req_bytes?: number;
  req_head?: string;
  resp_body?: string;
  resp_truncated?: boolean;
  remote_addr?: string;
  verdict: "ok" | "rejected" | "error";
  stage?: string;
  diag_code?: string;
  /** JSON-RPC response outcome; absent for non-RPC requests and old records. */
  proto_outcome?: MCPLogOutcome;
  /** tools/call result only. A truncated or missing response is unknown. */
  tool_outcome?: MCPLogOutcome;
}

// MCPLogStats aggregates the persistent history for the panel header.
export interface MCPLogStats {
  ring_len: number;
  total: number;
  by_verdict: Record<string, number>;
  by_transport: Record<string, number>;
  rejected_24h: number;
  dropped: number;
}
