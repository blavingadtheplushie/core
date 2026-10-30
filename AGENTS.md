# AGENTS.md — engineering guide for BLAVINGAD

This file is read by **BLAVINGAD itself** before it edits its own code (its
source lives in `$SOURCE_DIR`, default `/data/src`), and by any human or AI
working on the repo. `README.md` is for people deploying it.

---

## 0. Golden rules

1. **Never break the boot path.** `cmd/blavingad/main.go` → `serve()` must start
   even when integrations are missing or misconfigured. A missing token disables
   a feature; it never crashes the process. If the new binary crashes within 90 s
   of an upgrade, the supervisor rolls back, but don't rely on that.
2. **Small, tested changes.** Before `self_upgrade`: `self_build` must pass
   (`go mod tidy`, `go vet`, `go build`, `go test ./...`, smoke test `blavingad version`).
   Add or extend a test when you fix a bug.
3. **Every migration is append-only.** `internal/store/schema.go` holds an
   ordered `migrations` slice tracked by `PRAGMA user_version`. Never edit a
   shipped migration: append a new one (`ALTER TABLE … ADD COLUMN`, new tables).
4. **No secrets in code, logs, memory or commits.** Tokens come from the
   environment or Vaultwarden (`internal/vault`), are scrubbed from the process
   environment after `config.Load()`, and every known value is masked by
   `core.Secrets` (applied in `Runner.step`, `execTool`, model text and final
   output). Use secrets by reference (`vault://Item/field`) — never by value.
5. **Safety stays.** Never weaken: access profiles (tool filtering in
   `Runner.Run` *and* the re-check in `execTool`, memory modes, profile
   inheritance by sub-agents and schedules, `CanManageAccess` on access tools,
   the locked `admin` profile, fail-closed `Resolve`), approval for
   `core.RiskSensitive` tools, the `resolvePath` sandbox, the OIDC
   state/nonce/PKCE checks, the `Origin` check on mutating API calls, HTML
   escaping in the SPA.
6. **Everything ships with UI.** A backend feature gets a control in
   `internal/web/static/app.js`. Styling follows the Fold design system (tokens
   in `styles.css`, no shadows, no raw hex in components).
7. **Pure Go, no CGO.** SQLite is `modernc.org/sqlite`. Builds use `CGO_ENABLED=0`.

---

## 1. Process model

```
tini → blavingad supervise          (internal/selfupdate/supervisor.go)
          └─ <active binary> serve  (cmd/blavingad/main.go)
```

Two ways to run it, same layout (`/data`, `/workspace`, seed in
`/opt/blavingad/src`, base binary `/usr/local/bin/blavingad`):
- Docker/Podman image (`Dockerfile`): `tini` is PID 1, config from the env file.
- Proxmox LXC (`deploy/proxmox/create-lxc.sh`): systemd `blavingad.service`
  runs `supervise` **as root**, config in `/etc/blavingad/blavingad.env`,
  Docker engine inside the container. `blavingadctl update` runs the update
  with the service's environment. The prompt's "System" line tells which.

- Active binary: `/data/bin/blavingad` if present (self-built), else the image's
  `/usr/local/bin/blavingad`.
- `self_upgrade` builds into `/data/bin/blavingad.build`, commits, pushes to
  `SELF_REPO`, renames to `blavingad.next`, writes `pending.json`, then `serve`
  waits for active runs and exits with **code 42**.
- Humans update the same way without an image rebuild: `blavingad update
  [--pull] [--force]` (CLI, via `docker exec`) or Evolution → Update in the
  dashboard (`Manager.Update`). The CLI is a separate process: it asks the
  running agent to restart by writing `/data/bin/restart.request`
  (`WatchRestartRequests`). `/data/bin/built.sha` records the commit of the
  staged binary so "already up to date" can be detected.
- The supervisor promotes `.next` → current (old → `.prev`). A crash during the
  90 s probation restores `.prev` and writes `rollback.json`; the next boot turns
  it into a `lesson` memory and a Slack announcement.
- A rebuilt image (different base binary) discards the self-built binary.

## 2. Repo map

| Path | Role |
| --- | --- |
| `cmd/blavingad/main.go` | Wiring: config, store, memory, registry, tools, Slack, mail, scheduler, web, restarts |
| `internal/config` | Env config (immutable). Runtime knobs live in `agent/settings.go` (DB) |
| `internal/store` | SQLite open + migrations, settings, audit, generic `Rows/Row/Count` helpers, `FTSQuery` |
| `internal/llm` | OpenAI-compatible client: chat + tools, embeddings, retries |
| `internal/memory` | Hybrid memory (FTS5 bm25 + cosine, RRF), dedupe/merge, people, channel digests |
| `internal/core` | `Tool`, `Registry`, policies, `RunContext`, event `Bus`, `Approvals`, default persona, **access profiles** (`access.go`) |
| `internal/agent` | **The harness**: prompt assembly, tool loop, parallel tools, approvals, compaction, sub-agents (`delegate`), reflection, channel digests, self-review |
| `internal/tools` | All built-in tools + agent-authored custom tools |
| `internal/slackbot` | Socket Mode, message indexing, Block Kit layouts (`live.go`: working/final message, `blocks.go`: feedback row + reflection update, `interactive.go`: approvals, Stop/feedback buttons, Home tab), mrkdwn |
| `internal/mail` | IMAP polling + SMTP sending |
| `internal/cloudflare` | Cloudflare Tunnel API: tunnel lookup/creation, ingress routes (catch-all kept last), proxied DNS CNAMEs |
| `internal/vault` | Vaultwarden through the Bitwarden CLI: unlock, in-memory cache, `vault://` references, env resolution, save/generate |
| `internal/scheduler` | Cron / one-shot tasks |
| `internal/selfupdate` | Supervisor, build/upgrade/rollback, git plumbing |
| `internal/web` | HTTP server, OIDC + sessions, JSON API, SSE, embedded SPA in `static/` |

## 3. The harness loop (`internal/agent/runner.go`)

1. Insert a `runs` row (`running`), publish `run.started`.
2. System prompt = persona + operating rules + environment + skills index +
   recalled memories, person notes and channel digest + admin extra prompt.
3. Up to `max_iterations`: compact context if > 70 % of `LLM_CONTEXT_TOKENS`,
   call the model, record an `llm` step, run tool calls (calls that need no
   approval run in parallel, gated ones sequentially), append tool results.
4. On the last iteration, force a final answer without tools.
5. Persist output/status/tokens, publish `run.finished`, then reflect in the
   background (memory extraction) unless `NoReflect`.

Step kinds stored in `run_steps`: `llm`, `tool`, `approval`, `recall`, `reflect`,
`compact`, `access`. The web trace view (`stepItem` in `app.js`) renders each
kind; add a case there if you add a kind.

### Access profiles

Every Slack run carries a `core.Profile` resolved by `Access.Resolve(slackID)`:
`SLACK_ADMINS` → `admin`; whitelisted (`access_members`) → their profile;
everyone else → the `access.default_profile` setting. `Request.Profile == nil`
means a system run (web, admin schedules, self-review) with full access.
The profile filters the tool definitions sent to the model, is re-checked in
`execTool`, caps iterations, sets the memory mode (`none` skips recall entirely,
`read`/`none` disable reflection and memory-writing tools), adds a prompt
section, and decides approval rights (`any` / `own` / `none`).
Tool tokens: `*`, `cat:<category>`, `<tool_name>`, `-<token>` to exclude.

## 4. Adding a tool

```go
d.Reg.Add(&core.Tool{
    Name: "my_tool", Category: "web", Emoji: "🧪",
    Description: "What it does and WHEN to use it.",
    Params: core.Obj(map[string]any{"q": core.Str("…")}, "q"),
    Risk: core.RiskSafe,              // RiskSensitive ⇒ approval by default
    Available: func() bool { … },     // hide when not configured
    NeedsApproval: func(args json.RawMessage) bool { … }, // optional per-call refinement
    Summarize: summarizeField("Label : ", "q", 60),        // progress/approval label
    Handler: func(ctx context.Context, rc *core.RunContext, args json.RawMessage) (string, error) {
        a, err := core.Parse[struct{ Q string }](args)
        …
        return Clip(out, 0), nil
    },
})
```

- Register it from the matching `register*` function in `internal/tools`.
- Side effects outside the container (send, push, deploy, delete) ⇒ `RiskSensitive`.
- Return text the model can act on; clip long outputs with `Clip`.
- Write an audit entry (`d.St.Audit`) for anything a human would want to trace.
- New category? Add it to `CATEGORIES` in `app.js` (and remember profiles use
  `cat:<category>` tokens: a new category is invisible to non-`*` profiles).
- Tools reading memory or past conversations must be listed in
  `memoryReadTools` / `memoryWriteTools` (`core/access.go`) so memory modes apply.

For quick, script-level capabilities prefer `tool_create` (hot-loaded, no rebuild).

## 5. Web UI

- `internal/web/static/` is embedded with `go:embed`; editing it requires a rebuild.
- Vanilla JS SPA, hash router (`route()`), delegated `data-action` handlers,
  `esc()` for every interpolation, `md()` for model output (escape-first).
- New API route: add it in `Handler()` (`server.go`) wrapped with `a(...)`, and
  return `{ "error": "…" }` with a meaningful status on failure.
- Checks: `node --check internal/web/static/app.js`.

## 6. Commands

```bash
go vet ./... && go test ./...
node --check internal/web/static/app.js
CGO_ENABLED=0 go build -o /tmp/blavingad ./cmd/blavingad
ADMIN_PASSWORD=dev DATA_DIR=/tmp/blv WORKSPACE_DIR=/tmp/blv/ws LLM_BASE_URL=… LLM_MODEL=… /tmp/blavingad serve
```

## 7. Pitfalls

- `PRAGMA user_version` is the migration cursor: a migration that fails halfway
  rolls back its transaction; fix it and append, don't reorder.
- FTS tables are external-content (`content='memories'`): keep the triggers in sync
  when changing columns.
- Slack section blocks cap at 3000 chars: use `Chunk()`; never post raw markdown,
  use `ToMrkdwn()`. Every message is Block Kit (keep a plain `text` fallback
  for notifications); Slack rejects the whole message past 50 blocks or 10
  elements per context block — `live_test.go` checks the limits, extend it
  when you change a layout.
- The bot never adds emoji reactions and answers carry no buttons, no model
  name and no timings: status lives in the message (working layout → final
  layout). Only approval cards have buttons. Once decided, the card is deleted
  and the clicker gets a one-line ephemeral confirmation (`decidedText`);
  expired cards shrink to a single context line (`resolvedLine`). People stop a run with a 🛑
  reaction and rate answers with 👍 / 👎 (`onReaction`).
- The timeline (memories recalled, thoughts, tool calls, reflection) is a
  Slack `plan` block built by hand (`rawBlock` in `live.go`: tasks carry no
  `type`, `block_id` is stable for the whole run (`plan-<runID>`) so Slack keeps the block expanded, max 50 tasks — older ones are
  folded). Tool results of the `privateOutput` categories (memory, vault,
  email, access) are never rendered. If Slack answers `invalid_blocks`,
  `Bot.send` sets `noPlans` and falls back to the classic layout for good.
  The reflection task is completed by `watchReflections` on `run.reflected`.
- Product language is English (UI, tool texts, prompts, Slack). The agent still
  answers in the user's language (`AGENT_LANGUAGE` is only the default).
- `Runner.Run` must always end with a terminal status; the deferred block handles
  panics — keep it.
- The restart after `self_upgrade` waits for active runs (max 3 min). Don't start
  long work in the same turn as an upgrade.
- `resolvePath` allows workspace, source dir, data dir, `/tmp` and `$HOME` only.
- At boot `Manager.EnsureWritable` checks that every file of the source tree
  is writable; it repairs ownership/modes (root) or copies the tree to
  `/data/src-<timestamp>` and records it in `/data/source.path`, which
  `config.Load` follows when `SOURCE_DIR` is unset. Never hardcode `/data/src`:
  use `cfg.SourceDir`. If the old tree can't even be read (SELinux label of
  another container), it is rebuilt from `SELF_REPO` or `SEED_DIR` instead.
  Never copy into the volume with `cp -a`/`--preserve`: files from the image
  carry the current container's SELinux categories and become unreadable once
  the container is recreated. Use `cp -r --no-preserve=all`.
- A tool denied by the access profile emits a `tool_denied` progress event (🚫
  line in Slack and the web chat): keep it, it is how people tell an access rule
  from a real permission error.
- The `bw` CLI refuses plain-HTTP servers and takes ~5 s per call: everything
  reads from the in-memory cache; only `Open`, `Refresh`, `Save`, `Generate`
  and live TOTP shell out. `bw` gets a minimal environment — keep it that way.
- New secret-bearing setting? Add it to `config.VaultKeys` (and `secretEnv` /
  `SecretValues` if it is a secret) so it can come from the vault, is scrubbed
  and is masked.
- Any tool parameter that carries a `vault://` reference must make
  `NeedsApproval` return true and resolve through `Deps.secretEnv`/`Deps.expand`
  (which enforce the access profile and register values for masking).
