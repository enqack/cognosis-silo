# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this silo. It is
the agent-facing operating contract — your behaviors, in-session capture, and tooling. The immutable
mechanics (vault layout, the knowledge lifecycle and its thresholds, the frontmatter contract) are
defined once in **[SILO_MECHANICS.md](SILO_MECHANICS.md)** — the single source of truth this file links
to rather than restates, so the human and the agent never drift onto different numbers.
[HUMAN.md](HUMAN.md) is the operator's digest and [README.md](README.md) is the newcomer's orientation;
both also defer to SILO_MECHANICS.md for the rules.

## What this is

This is a **Multi-Project Silo** — a coordination center for independent agentic projects, not a single
codebase. A human operator and AI agents (you) share long-lived context here: the projects the silo
coordinates, working theories, settled decisions, and the tooling to search all of it.

The projects coordinated in this silo are registered in [PROJECTS.md](PROJECTS.md) — consult it for the
current list, locations, and remotes, and add a row there when onboarding a project. (`silo-kb reset`
restores `PROJECTS.md` to its empty template along with the vault.)

## Layout & tiers

The full vault layout — the markdown tree, the two-knowledge-tiers-plus-two-raw-capture model, the
`type` taxonomy, and the `knowledge/` subdirectory layout — is defined in
**[SILO_MECHANICS.md § Layout & tiers](SILO_MECHANICS.md#layout--tiers)**. What matters for how you
work:

- **The signal is the directory.** `daily/` and `deep-thoughts/` are append-only raw capture;
  `knowledge/**` is working theory that decays; `projects/**` is asserted canon that doesn't. Where you
  put a note *is* its epistemic status.
- **The markdown is the single source of truth.** Postgres is a derived, droppable index. **You cannot
  corrupt canonical knowledge through the database:** you retrieve read-only through the
  `query_knowledge` MCP tool and never write Postgres directly, and the index rebuilds from the working
  tree (`silo-kb reindex --full`) at any time. Everything durable lives in version-controlled markdown.

## The lifecycle

Knowledge is reinforced, decays, falsifies, archives, and graduates on `/kb-compile` runs — full rules
and thresholds in **[SILO_MECHANICS.md § The lifecycle](SILO_MECHANICS.md#the-lifecycle)**. Your part
is the **agent-justified** moves: reinforcement, falsification (`--falsify`), dispute (`--dispute`), and
graduation (`--graduate`) only happen when you explicitly justify them — mere mention never counts and
nothing is inferred. Reinforcing and falsifying/disputing the same note in one run is rejected as a
contradiction (resolve it, don't expect one to silently win), and reinforcing a disputed note clears the
dispute. Decay and archival are automatic — and a note still cited by recently-committed work is
refreshed automatically, so you don't need to fake-reinforce to keep live theory alive. You don't
hand-edit confidence or maturity, and you never touch `knowledge/log.md` (the compile audit trail).

## The frontmatter contract

Every note you write must satisfy the contract in
**[SILO_MECHANICS.md § The frontmatter contract](SILO_MECHANICS.md#the-frontmatter-contract)** —
required `type` + stable `id`, the extra decay fields under `knowledge/**`, no decay fields under
`projects/**`, `YYYY-MM-DD HH:MM:SS` timestamps, wikilinks for vault-internal links and plain relative
links for real repo files. The **PreToolUse `silo-kb validate` hook enforces this on every write** and
blocks a violation with a correction message, so heed it rather than guessing. Never hand-edit the two
generated files: `knowledge/index.md` (`silo-kb sync-index`) or `knowledge/log.md` (`silo-kb compile`).

## Tooling & commands

Dev environment: `nix develop` auto-starts the silo — the shellHook runs `silo-init` (idempotent full
bootstrap: git init, vault scaffold for a fresh silo, Go build, Postgres via `pg-start`, Ollama via
`ollama-start` + model pull, reindex + sync-index) and then prints a status/command banner via
`silo-help`. Set `SILOKB_NO_AUTOSTART=1` to skip the bootstrap; `pg-start`/`pg-stop`/`pg-nuke` manage
Postgres directly; `ollama-start`/`ollama-stop` manage the embedding server. Ollama is bundled
(`pkgs.ollama`) but portable: `ollama-start` reuses a server already listening on `:11434` — a native
macOS app (Metal) or a NixOS `services.ollama` — and only starts the bundled one as a fallback. DSN is
exported as `SILOKB_DSN`. Embeddings come from local Ollama (`nomic-embed-text:v1.5`, version-pinned);
changing the embedding model (or its tag) requires a full reindex — `silo-kb reindex --full` — since
old and new embeddings aren't comparable. Manual build: `cd tools/silo-kb && go build -o silo-kb .`

The commands split by interface, which mirrors the safety boundary: your default reach into the vault
is the **read-only** MCP retrieval tool, while everything that mutates the vault or the index is an
administrative CLI command (human- or slash-command-driven, some destructive).

**Agent retrieval (read-only, via MCP):**

| Command | Purpose |
|---|---|
| `query_knowledge` (MCP tool) | hybrid RRF retrieval (semantic + keyword + graph leg) — your default way to read the vault; never touches Postgres directly. Falsified notes are excluded unless `include_falsified: true` |
| `silo-kb serve-mcp` | stdio MCP server that exposes `query_knowledge` |

**Administrative CLI (human- or slash-command-driven):**

| Command | Purpose |
|---|---|
| `silo-kb query "text" [--project P] [--top-k N] [--include-falsified]` | hybrid RRF retrieval from the shell (the CLI form of `query_knowledge`) |
| `silo-kb reindex [--full]` | delta-sync the vault into Postgres (chunks + embeddings + link graph) |
| `silo-kb compile [--dry-run] [--reinforce …] [--falsify …] [--dispute …] [--supersede …] [--graduate …]` | knowledge lifecycle run |
| `silo-kb sync-index` | regenerate `knowledge-base/knowledge/index.md` |
| `silo-kb inject-index --budget N` | truncated index for SessionStart injection |
| `silo-kb validate` | check the whole vault against the frontmatter contract (also the PreToolUse/pre-commit gate) |
| `silo-kb migrate` | apply the derived-index schema idempotently (reindex runs it automatically) |
| `silo-kb reset --force` | **destructive** — wipe the vault back to the fresh-silo scaffold, then rebuild the index (git is the recovery net) |

The destructive commands (`silo-kb reset --force`, and the `pg-nuke` shell command) affect only the
markdown scaffold and the *derived* index — never a source of truth that isn't already in git. Slash
commands: `/kb-reindex`, `/kb-query`, `/kb-compile`, `/kb-sync-index`, `/kb-reset`.

## Automatic session behavior

The two raw-capture tiers are meant to be **written by the agent when a situation calls for one**, not
supplied by the operator. Don't wait to be asked.

- **Session start:** a truncated knowledge index is injected into context (knowledge/projects entries
  survive truncation first).
- **On writes:** the frontmatter validator hook (see The frontmatter contract) blocks violations with a
  correction message.
- **Daily log — capture in-session.** As durable material surfaces, append it to
  `knowledge-base/daily/YYYY-MM-DD.md` (today's date). The log is **time-primary**: each capture pass
  is its own `## HH:MM:SS` block (local time, from `date "+%H:%M:%S"` — time-only, the file is already
  dated), and the categories are `###` subsections *inside* that block, created only when you have
  entries for them: `### Concepts` (durable ideas/decisions), `### Cursed Knowledge` (surprising
  gotchas), `### Unresolved` (open questions as `- [ ]` checkboxes), `### Log` (one-line summaries of
  what was done). Categorized bullets only — no freeform prose. Append-only: reuse the current pass's
  `## <time>` block while you're still in that pass; a later capture moment appends a new `## <time>`
  block. If the file doesn't exist, create it with frontmatter — a fresh lowercase `uuidgen` `id`,
  `type: daily-log`, `title: <date>`, `timestamp: YYYY-MM-DD HH:MM:SS` — then an h1 `# <date>`, then
  the first `## <time>` block. This is raw capture — never graduate directly into `knowledge/` or
  `projects/` (that stays a deliberate `/kb-compile` act).
- **Deep thoughts — generate at notable moments (moderate cadence).** When a session hits a notable
  milestone or a vivid moment of friction or triumph, write a grounded reflection. Multiple per session
  are fine; don't force one when nothing notable happened. **Operational constraints (owned here):**
  write the file to `knowledge-base/deep-thoughts/YYYY-MM-DD-HH-MM-{slug}.md` (slug = lowercase topic,
  spaces→hyphens, punctuation stripped; timestamps from `date "+%Y-%m-%d-%H-%M"` and
  `date "+%Y-%m-%d %H:%M:%S"`), with OKF frontmatter — `id` via `uuidgen`, `type: deep-thought`,
  `title`, `timestamp: YYYY-MM-DD HH:MM:SS`, and a **required `description`**: one dry, literal sentence
  stating the actual session event (the grounded reflection you already do to write the joke — e.g.
  "Added passive citation-refresh to the compile lifecycle"). The indexer embeds **only** the
  `description`, never the comedic body, so this is what makes the note findable; a joke-only file would
  scatter itself across the vector space. Then write the thought as a blockquote (Write tool, never echo
  or heredoc), and display it to the user with its path. **For the voice**, follow
  [prompts/deep-thoughts-persona.md](prompts/deep-thoughts-persona.md) — the silo's self-contained
  source of truth for the style (no skill required; if the user-level `deep-thoughts` skill is
  installed, its `/deep-thoughts` command also works).
- **Session end:** the `SessionEnd` hook (`session-end-extract.sh`) is a headless backstop that appends
  any still-missing categorized notes from the session to today's daily log (requires a one-time
  `claude /login`; writes only to `daily/`). In-session capture need not be exhaustive — capture what's
  clearly durable as you go and let the hook catch the rest.

## Adding a project

Register the project in the table in [PROJECTS.md](PROJECTS.md), then create
`knowledge-base/projects/<project-name>/` following the same shape as existing projects: flat
project-level notes (overview, build tooling, testing, conventions, etc.), an `architecture/`
subdirectory (one note per major subsystem/component), and a `concepts/` subdirectory for cross-cutting
patterns referenced from multiple notes.

