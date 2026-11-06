# Multi-user session isolation

Reviewed against the working tree on 2026-09-29. This page describes account and API ownership, not verified tenant process containment. See [current status](project-status.md) and [security boundaries](security.md).

One server, two modes. With **no users file configured**, pogo runs the
classic single-user shared-token server and nothing about it changes. With
`POGO_USERS_FILE` armed (password login, users.json) — the same full-featured
server switches to **per-user session isolation**: every user gets their own
session space, workspace and memory. The deployment web token is mapped to the admin account rather than providing an anonymous shared session space.

(The former delegated-JWT channel — `POGO_IDENTITY_CONFIG`, business backends
signing Ed25519 credentials for their users — has been removed. Local accounts
and their fixed tokens are the only identity source now.)

Users share the server runtime. Studio definitions select capabilities and pin a conversation snapshot; MCP connections can use account-specific credentials. These mechanisms do not establish a private operating-system environment for each account.

## What isolation means, precisely

- **Sessions.** Ownership lives in an authoritative SQLite registry
  (`catalog.db`, beside the transcripts). Listing, reading, renaming,
  deleting, messaging and streaming all answer through the owner's scope;
  another user's session id returns 404 exactly like a missing one. Session
  ids in this mode carry a 128-bit random suffix and are globally unique —
  one id never names two owners, even after deletion (tombstone).
- **Workspace.** Each user's sessions resolve workspace requests under
  `<server root>/users/<subject prefix>/`. Studio adds a default `agents/<id>`
  directory. File tools, uploads and file-panel requests apply path guards;
  starting a shell in this directory does not by itself confine that shell.
- **Memory.** Per user, under their own root (`<root>/.pogo/memory`). One
  user's notes are not injected into another user's system prompt or granted
  through its file-tool write roots. Memory remains shared across one user's Agents.
- **Checkpoints and the file journal.** Per-user instances, keyed by the
  owner's root: rewinding files or reviewing workspace changes touches only
  that user's tree.
- **Operator surfaces.** The configuration, MCP, sandbox, skills-upload,
  starters, panels, scheduler and log endpoints answer only to the admin
  grant: the default admin account, or accounts flagged `"admin": true` in
  users.json.
  Ordinary accounts can use the published Agent surfaces, Agent-private workflows and their own
  session space, but cannot use administrator-only configuration routes.
  Conversation surfaces (models, skills listing, tools, attachments policy)
  stay open to every authenticated user.
- **Tokens are bound to accounts and never expire.** In multi-user mode
  `POGO_WEB_TOKEN` maps to the default **admin** account (identity, session
  space and the operator grant); every other account can carry its own fixed
  token in users.json (`"token"` field, minted with `pogo -gen-token`) and
  enters exactly that account's space via `Authorization: Bearer` or
  `?token=`. Rotating a token is editing the file and restarting. In
  single-user deployments (no identity sources) the shared token keeps its
  classic meaning unchanged.
- **pogo-as-MCP server (`/mcp`, `/sse`)** keeps its own operator-configured
  machine credential. Its tasks run unowned and are invisible in user lists;
  it is an operator integration, not a per-user surface. Agent-scoped MCP also
  checks the selected Agent and its MCP-export binding; machine callers cannot
  use that binding to continue an owned Web conversation.

## Process boundary still requiring work

The current ordinary session registry does not pass the available strict
`tools.CommandScope` to Bash. `Session.Terminal` calls `startTerminal`, which
passes a nil scope. The generic Linux sandbox exposes a read-only view of the
filesystem and makes configured roots, including HOME, writable; masking the
private Skill store is a separate protection. Therefore API ownership and
separate default directories must not be advertised as full cross-user shell
or terminal isolation. This is a static finding; live cross-user containment
has not been accepted in this review.

## Accounts and fixed tokens (users.json)

Password login and fixed tokens are two shapes of the same account table.
Manage it by editing `POGO_USERS_FILE` (writable-mounted in deployments) —
each row is one account:

```json
{
  "users": [
    {"username": "admin",  "hash": "pbkdf2-sha256$…", "created": "2026-09-11"},
    {"username": "ywapi",  "hash": "pbkdf2-sha256$…", "token": "kM8…32chars", "created": "2026-09-11"},
    {"username": "ops",    "hash": "pbkdf2-sha256$…", "admin": true}
  ]
}
```

- **Passwords**: `pogo -hash-password` (reads stdin) prints a paste-ready
  line. Change from the UI's settings dialog writes through to the file.
- **Fixed tokens**: `pogo -gen-token` prints a candidate; paste it into the
  account's `"token"` field and restart. No expiry by design — rotation is
  editing the value. External systems integrate with
  `Authorization: Bearer <token>` or `?token=<token>` on any API call, or
  with username/password via `POST /api/login` (HttpOnly cookie follows).
- **Admin**: the `admin` account (or `"admin": true`) holds the operator
  grant; `POGO_WEB_TOKEN` is bound to it.
- The gate page offers both shapes: the token field always, the
  username/password form one toggle away when accounts exist.
- **Self-provisioning**: `POST /api/login` with an unknown username and an
  extra `admin_password` field (the admin account's password, or the
  deployment token — the same operator key in its two spellings) creates the
  account on the spot and logs it in. This is the external-system onboarding
  flow: send url + username + password (+ admin key) and the account exists.
  Rules: 8–128 character password (rune count), sane username (no control
  characters, not the reserved `admin`), the new account is an ordinary user,
  every failed attempt counts into the per-IP login lockout, and failures
  answer a uniform 401 that does not reveal which check failed.

## Legacy sessions

Sessions written before isolation (CLI-era transcripts, files copied in) have
no ownership row and are **invisible** in multi-user mode — ownership is
never inferred from a file's presence. Before deciding who owns legacy
records, take a read-only inventory (ids, sizes, hashes; no conversation
bodies):

```powershell
node scripts/session-ownership-inventory.mjs C:/legacy/sessions > ownership-inventory.json
```

Backfilling owners onto existing transcripts is the explicit migration stage
(`catalog.Create` per row, from the inventory's `owner_mapping`); it has not
been automated in this batch.

## Known limits of this batch

- No per-user run/stream/session quotas yet (the first batch's counters went
  with its restricted server; they return as generic limits if the business
  needs them).
- No general immediate user-disable/revocation callback. Fixed account tokens
  do not expire; rotate them through the account configuration and restart.
  There is no current business-signed short-lived JWT channel.
- Ordinary scheduled prompt jobs run as CLI child processes whose transcripts
  land unowned. Agent-bound schedules use the Web Agent runtime with their
  existing owner and Agent bindings. Retired platform-workflow schedules are
  excluded rather than converted into ordinary prompt jobs.
- The old delegated embed bridge is not a supported integration contract.
  External systems must use the current local-account/token API or explicitly
  design and implement a new identity integration.
