# 🤖 BLAVINGAD

**An autonomous AI agent for Slack** that reads everything going on, answers
when mentioned, searches the web, codes, deploys, sends emails, remembers
everything… and **improves itself**, all the way to rewriting and redeploying
its own code.

A single Docker container, written in Go, with a web dashboard in the Fold
design system, protected by OIDC.

---

## ✨ What it can do

| | |
| --- | --- |
| 💬 **Slack** | Socket Mode (no public URL needed). Reads and indexes every channel it's in, answers @mentions, DMs and threads it takes part in. Every message is a Block Kit layout, with no buttons and no emoji reactions from the bot. On top of each answer, a collapsed **Thought process** (a native Slack `plan` block) lists in chronological order the recalled memories, each thinking step, each tool call with its arguments and result, and finally the **Reflection** (what it memorized). Results of memory, vault, email and access tools are never shown in Slack. Workspaces that reject plan blocks automatically get a compact classic layout. The web chat shows the same collapsible panel. React 🛑 to stop a run, 👍/👎 to rate an answer. Home tab. |
| 🔎 **Web** | Exa semantic search, sourced answers, page reading, arbitrary HTTP requests. |
| 🧠 **Memory** | Typed memories (facts, preferences, lessons, procedures…) with **hybrid** search (FTS5 + embeddings, RRF fusion), automatic merging of duplicates, per-person notes, rolling channel digests, and **automatic reflection** after every conversation. |
| 💻 **Code** | Shell inside the container (git, gh, go, node, bun, python, wrangler, docker), file read/write/edit, ripgrep search. |
| 🐙 **GitHub** | Its own account: full REST API, search, repository creation, git push. |
| 📬 **Email** | Its own address (IMAP/SMTP): it reads, summarizes, replies — and can handle incoming mail on its own. |
| 🚀 **Deployment** | Cloudflare Pages & Workers, Docker containers on the host (with optional Traefik + HTTPS), GitHub Pages. |
| ⏰ **Tasks** | Reminders and cron routines it runs with all of its tools. |
| 🧑‍🚀 **Sub-agents** | It delegates subtasks to sub-agents working in parallel. |
| 🧬 **Self-improvement** | Rewrites its persona, builds up skills, **creates new tools for itself** that are hot-loaded, and **modifies its own Go code** (build + tests → commit → push → restart, with **automatic rollback** if the new version crashes). Daily review of its mistakes and 👎 feedback. |
| 🛂 **Allowlist** | Access profiles (👑 Admin, 🙂 Member, 👋 Guest, 🚫 Blocked, + your own): allowed tools, full / read-only / no memory, approval rights, step limit, dedicated instructions. People not on the list get the default profile. Configurable in the web UI **or by asking it**. |
| 🔐 **Vaultwarden vault** | Tokens and passwords (Cloudflare, GitHub, LLM, email…) read from Vaultwarden, synced every 15 min (automatic restart if one is rotated). The agent uses them **by reference** without ever seeing the values, files new secrets away itself, and every known secret is masked as `••••••` everywhere. |
| ✋ **Guardrails** | Sensitive actions (shell, deploy, email, GitHub writes…) require approval through Block Kit cards in Slack (**Approve** / **Deny** / **Always allow**) or in the web UI. Per-tool policy: *Auto / Approve / Off*. Read-only commands go through without asking. |
| 🖥️ **Dashboard** | Overview, web chat, **full harness trace** for every run (model calls, tools, approvals, recalled memories, reflection), editable memory, tools, persona & skills, evolution, tasks, deployments, mailbox, log, settings. Live via SSE. Light/dark, mobile-friendly. |

---

## 🏗️ Architecture

```
                         ┌──────────────── container ─────────────────┐
 Slack (Socket Mode) ◀──▶│  slackbot ─┐                                │
 Browser (OIDC)      ◀──▶│  web/SSE ──┤                                │
 IMAP / SMTP         ◀──▶│  mail ─────┼──▶  agent (harness)  ──▶ LLM   │
 Cron                    │  scheduler ┘     │  prompt + memory         │
                         │                  │  tool loop ∥             │
                         │                  │  approvals ✋            │
                         │                  │  compaction, delegation  │
                         │                  │  reflection 🧠           │
                         │                  ▼                          │
                         │   tools: web · slack · memory · shell ·     │
                         │   files · github · email · deploy ·         │
                         │   tasks · self (code, persona, skills,      │
                         │   hot-loaded custom tools)                  │
                         │                                             │
                         │   SQLite (/data) · workspace (/workspace)   │
                         │   supervisor ⇄ self-built binary            │
                         └─────────────────────────────────────────────┘
```

The entry point is `blavingad supervise`: a supervisor starts the agent,
swaps the binary when the agent has rebuilt itself, and reverts to the
previous version if the new one crashes on startup. Technical details in
[`AGENTS.md`](AGENTS.md).

---

## 🚀 Installation

### 1. The Slack app

1. <https://api.slack.com/apps> → **Create New App** → **From a manifest** →
   paste [`slack-app-manifest.yml`](slack-app-manifest.yml).
2. **Basic Information → App-Level Tokens**: create a token with the
   `connections:write` scope → `SLACK_APP_TOKEN` (`xapp-…`).
3. **Install App** → `SLACK_BOT_TOKEN` (`xoxb-…`).
4. Invite it to channels: `/invite @BLAVINGAD`. It reads the recent history
   and summarizes it.
5. Grab your Slack ID (profile → ⋯ → *Copy member ID*) for `SLACK_ADMINS`,
   and a channel ID for `SLACK_HOME_CHANNEL`.

### 2. The model

Any API with tool calling, in one of two formats (`LLM_API_STYLE`, also a
toggle in **Settings → Model**):

- **OpenAI-like** (`/chat/completions`): OpenAI, OpenRouter, Ollama, LiteLLM,
  vLLM, Groq, Mistral…
- **Anthropic-like** (`/messages`): Anthropic, and gateways that speak the
  Messages API.

Set `LLM_BASE_URL`, `LLM_API_KEY` and `LLM_MODEL` (extra headers some
gateways require go in `LLM_HEADERS`). **Settings → Model** lists the models
the endpoint offers and has a **Test connection** button that shows the
provider's exact error — handy for "unknown model" mistakes. For semantic
memory, add an embeddings model (`EMBED_MODEL`, e.g.
`text-embedding-3-small`; embeddings always use the OpenAI format).

The agent speaks English by default (`AGENT_LANGUAGE`) and still replies in
the user's language.

### 3. Its identity

- **GitHub**: create an account for it and a token (scopes `repo`, `workflow`) →
  `GITHUB_TOKEN`, `GITHUB_USER`. Create an empty `SELF_REPO` repository (e.g.
  `blavingad-bot/blavingad`): it will push every self-improvement there.
- **Email**: a dedicated mailbox (Migadu, OVH, Gmail with an app password,
  Proton Bridge…) → `IMAP_*`, `SMTP_*`, `EMAIL_FROM`.
- **Search**: an [Exa](https://exa.ai) key → `EXA_API_KEY`.
- **Cloudflare**: an API token (Workers Scripts + Pages: Edit) →
  `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`.

### 4. The web interface (Pocket ID)

In Pocket ID → **OIDC Clients** → new client:

- Callback URL: `https://<PUBLIC_URL>/api/auth/callback`
- PKCE: enabled

Then set `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, and restrict
access with `OIDC_ALLOWED_EMAILS` or `OIDC_ALLOWED_GROUPS`.
Any OIDC provider works (Authentik, Keycloak, Google…).

### 5. Run it

```bash
cp .env.example .env    # then fill it in
docker compose up -d --build
docker compose logs -f
```

The dashboard listens on port `8080` (put it behind your reverse proxy with
HTTPS and set `PUBLIC_URL`).

---

## 🧬 How it improves itself

| Level | Tool | Effect |
| --- | --- | --- |
| Memory | `memory_save`, automatic reflection | Remembers facts, preferences, lessons |
| Personality | `persona_update` | Rewrites its persona (versioned, restorable) |
| Know-how | `skill_save` | Saves a reusable procedure |
| Capabilities | `tool_create` | Writes a bash/python/node script that becomes a **new tool**, no restart needed |
| Code | `fs_edit` → `self_build` → `self_upgrade` | Modifies its Go code, compiles, tests, commits, pushes, restarts; automatic rollback on crash |

Every night (configurable), a **self-review** analyzes errors, 👎 feedback,
failing tools and denied actions, then applies at most two improvements and
posts a report in the home channel. Everything is visible under
**Evolution** and reversible (*Previous version* button).

> A Docker image you rebuild always takes precedence: the self-built binary
> is then discarded (its history stays in git).

---

## ⬆️ Updating without rebuilding the image

The container rebuilds BLAVINGAD itself from its source in `/data/src` (a
persistent volume, a git clone of `SELF_REPO`), then the supervisor swaps the
binary — with the same automatic rollback as self-upgrades if the new version
crashes. Go code and the web UI (embedded in the binary) never need a
`docker build`:

```bash
# you pushed to SELF_REPO → pull, build, test, restart (≈1 min)
docker exec blavingad blavingad update --pull

# you copied files into the container yourself
docker cp ./internal blavingad:/data/src/
docker exec blavingad blavingad update

# rebuild even if nothing changed
docker exec blavingad blavingad update --force
```

Or use **Evolution → Update from GitHub / Rebuild from source** in the
dashboard, which streams the build log live. The build runs the tests first:
if they fail, nothing is deployed. Running conversations finish before the
restart.

Rebuild the image only when the **system** changes (Dockerfile, new apt/npm
packages such as the Bitwarden CLI). A rebuilt image always wins over a
self-built binary.

## 🔐 Secrets in Vaultwarden

Only the Slack tokens and access to the vault itself stay in `.env`.
Everything else can come from [Vaultwarden](https://github.com/dani-garcia/vaultwarden):

1. Create a **dedicated account** for the bot on your Vaultwarden (or share an
   organization with it), then its **API key**: *Settings → Security → Keys →
   View API key* → `VAULT_CLIENT_ID`, `VAULT_CLIENT_SECRET`, and its master
   password → `VAULT_MASTER_PASSWORD`.
2. In a `BLAVINGAD` folder, create an item **named after the variable**
   (e.g. `CLOUDFLARE_API_TOKEN`, password = the token) and leave the variable
   empty in `.env`. Or reference any item:
   `CLOUDFLARE_ACCOUNT_ID=vault://Cloudflare/account_id`.
3. On startup it unlocks the vault, loads the config, then **removes every
   secret from its environment** (its shell doesn't inherit them).

> **"The decryption operation failed" / "Vault is locked"**: the master
> password doesn't decrypt the account's vault. Make sure the API key and the
> password belong to the **same** account, write `$` as `$$` with docker
> compose, and don't quote the value with `--env-file`. A stale cached login is
> replaced automatically; then click **Sync** in Settings (or restart).

Supported variables: `LLM_API_KEY`, `EMBED_API_KEY`, `EXA_API_KEY`,
`GITHUB_TOKEN`, `GITHUB_USER`, `IMAP_*`/`SMTP_*` (usernames and passwords),
`EMAIL_FROM`, `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`,
`OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `SESSION_SECRET`, `ADMIN_PASSWORD`.

If the bot can read the vault but not write to it, it is usually one of two
things, and `vault_save` now says which: an organization policy *Remove
individual vault* (set `VAULT_COLLECTION` to a collection it can edit — with a
single shared collection it is used automatically), or a collection shared
with it as *Can view* (give it *Can edit*). Settings → Vaultwarden shows where
new items go.

**On the agent side**, the vault is a tool (restricted to 👑 Admin by default):

| Tool | Effect |
| --- | --- |
| `vault_search` | Lists items and their references, **never the values** |
| `shell` → `secrets` | `{"PGPASSWORD": "vault://Postgres prod/password"}`: injected into the command |
| `http_request` | `"Authorization": "Bearer {{vault://CLOUDFLARE_API_TOKEN}}"` |
| `vault_save` | Stores a new token/password (or generates one), as a login or a secure note — in the bot's personal vault, or in an organization collection (`VAULT_COLLECTION` or a `collection` argument) |
| `vault_reveal` | Shows a value **to the human** in an ephemeral Slack message, not to the model |

Every use of a secret requires approval (the request shows the reference, not
the value), even for a simple `GET`: that's the classic exfiltration path. And
every value known from the vault or the config is replaced with `••••••` in
whatever the model, the database, Slack and the web UI see.

> ⚠️ Masking protects against accidental leaks, not against deliberate
> encoding (base64…): that's why injection still requires human approval.

---

## 🛂 Who can talk to it

Every Slack message is tied to an **access profile**:

| Profile | Default |
| --- | --- |
| 👑 **Admin** (`admin`) | Everything: all tools, full memory, approves everything, manages access. `SLACK_ADMINS` are always admin. |
| 🙂 **Member** (`member`) | Everything except access management, the vault and self-modification. Approves their own requests. |
| 👋 **Guest** (`guest`) | Web search only, **no memory** (nothing is recalled or retained), 12 steps max, instructed not to disclose anything internal. This is the profile for people **not on the allowlist**. |
| 🚫 **Blocked** (`blocked`) | It doesn't reply (messages are still read and indexed). Make it the default profile to only answer the allowlist. |

Everything is configured under **Access** in the dashboard, or by simply
talking to it (admins only):

> @BLAVINGAD give @alice the member profile
> @BLAVINGAD create a "client" profile with just web search and read-only memory, and put @bob in it
> @BLAVINGAD from now on, ignore people who aren't on the allowlist

The profile is enforced **in the harness**: the model only sees the allowed
tools, every call is re-checked at execution time, and it is inherited by
sub-agents and by scheduled tasks the person creates (no privilege escalation
that way). Approvals follow the profile too: *approves everything*, *their own
requests* or *nothing*. The **Always allow** button is reserved for admins.

Profile tool syntax: `*` (everything), `cat:web` (a category), `web_search`
(a tool), and `-…` to exclude, e.g. `["*", "-cat:self", "-shell"]`.

---

## 🔐 Security — read this

- **Autonomous self-modification is powerful**: it lets it run arbitrary code
  in its container without approval. That's the "fully autonomous" choice. To
  rein it in, set `self_upgrade` (and `self_build`) to *Approve* under **Tools**.
- The **Docker socket** grants root access to the host. Remove the mount in
  `docker-compose.yml` if you don't need Docker deployment.
- Give it **dedicated** accounts (GitHub, email, Cloudflare) with minimal
  permissions.
- Slack approvals follow access profiles: admins approve everything, members
  their own requests, guests nothing. The **Always allow** button (which
  switches the tool to automatic) is reserved for admins. With no admin or
  member declared, approvals are only possible from the web UI.
- The web interface: OIDC with PKCE + state + nonce, `HttpOnly`/`SameSite`
  cookies, origin check on mutating requests, strict CSP for scripts.

---

## 🛠️ Development

```bash
go test ./... && go vet ./...
node --check internal/web/static/app.js

# locally, without Slack, with password login:
ADMIN_PASSWORD=dev DATA_DIR=/tmp/blv WORKSPACE_DIR=/tmp/blv/ws SEED_DIR=$PWD \
LLM_BASE_URL=https://openrouter.ai/api/v1 LLM_API_KEY=… LLM_MODEL=… \
WEB_ADDR=:8080 go run ./cmd/blavingad serve
```

Structure, conventions and pitfalls: [`AGENTS.md`](AGENTS.md).
