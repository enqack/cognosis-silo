# Cognosis — Architecture Draft

**Status:** Design phase, not yet implemented
**Reference implementation:** `silo-kb` (informs mechanics; not imported as a dependency)

## 0.1 Scope: v1 MVP vs. Deferred

**In scope for v1 (§1–11, §12–15):**
- Single-daemon, local or remote, MCP interface + CLI
- Frontmatter contract, compile lifecycle, RRF retrieval
- Token auth, audit logging, Postgres + Ollama
- FileWatcher reconciliation for out-of-band edits
- Two-tier persona discovery
- Platform-native service files (optional)
- Cross-model retrieval, temporal queries, generative summaries
- Vectorized frontmatter, cross-project graph
- Git commit integration (optional, opt-in hook)
- Retrieval-augmented compilation

**Explicitly deferred to post-v1:**
- Slack/Discord bridge (§15)

All v1 extensions (§12–15 except Slack) are structured so they can be adopted incrementally without reshaping the core: new optional params, new metadata tables, no schema-breaking changes.

## 1. Purpose

Cognosis is a centralized, project-agnostic, long-term memory service for Claude Code (and other MCP-capable LLM backends). It runs as a background daemon on the machine, exposes an MCP interface, and gives any AI coding agent persistent, cross-project memory instead of per-repo, siloed knowledge.

Where `silo-kb` treats each repository as its own self-contained knowledge base (git-integrated, repo-relative, CLI-first), Cognosis inverts that: one knowledge base, many projects, accessed as a service rather than cloned per repo.

## 2. Relationship to silo-kb

`silo-kb` remains an exploratory reference project. Cognosis does **not** import its Go packages as a dependency. Instead, Cognosis is a fresh implementation that ports the *mechanics*:

- Frontmatter contract (id, confidence, maturity, sources)
- Compile lifecycle (reinforcement, decay, archival, graduation)
- Chunking strategy and embedding task prefixes
- RRF (reciprocal rank fusion) retrieval combining vector + full-text + graph
- Link graph for provenance and note-to-note relationships

And deliberately drops or changes what doesn't fit a centralized, service-based model:
- No git integration for staleness — replaced with explicit frontmatter timestamps
- No Nix dev-shell-per-repo bootstrap — replaced with `flake.nix` for one-time installation only
- No per-project repo/silo separation — replaced with a single KB + `project` metadata tag

## 3. High-Level Architecture

```
                    ┌──────────────────────────┐
                    │      Cognosis Service        │
                    │  (background daemon)       │
                    │                             │
                    │  ┌───────────────────────┐  │
                    │  │   MCP Server            │  │
                    │  │  (tools/call surface)   │  │
                    │  └──────────┬────────────┘  │
                    │             │                │
                    │  ┌──────────▼────────────┐  │
                    │  │  Core Logic            │  │
                    │  │  - write / query        │  │
                    │  │  - compile lifecycle    │  │
                    │  │  - chunk / embed        │  │
                    │  └──────────┬────────────┘  │
                    └─────────────┼────────────────┘
                                  │
              ┌───────────────────┼───────────────────┐
              │                   │                   │
     ┌─────────────────────┐ ┌──────────────────┐ ┌──────────────────┐
     │  $XDG_DATA_HOME/    │ │   Postgres       │ │   Ollama /       │
     │  cognosis/kb/       │ │   (managed,      │ │   remote         │
     │  (markdown          │ │   derived index) │ │   embeddings API │
     │  source of truth)   │ │                  │ │                  │
     └─────────────────────┘ └──────────────────┘ └──────────────────┘
```

MCP clients (Claude Code today; other MCP-compliant backends by design, since MCP is a cross-vendor standard adopted by OpenAI, Google, Microsoft, and others) connect to a single Cognosis MCP server via Streamable HTTP. No per-backend adapter layer is needed — MCP's shared spec covers this. The main portability caveats:

- Stick to `tools/call` as the lowest common denominator; some clients only partially implement `resources`/`prompts`
- Build on Streamable HTTP transport (the emerging cross-vendor standard; stdio/SSE is being phased out)
- Expect small per-client auth shims if/when OAuth flows differ across vendors — not a full adapter architecture

## 4. Storage

### 4.1 Markdown files — source of truth
- Location: flat directory, `$XDG_DATA_HOME/cognosis/kb/` (§9.2)
- Cognosis is the sole writer to this directory (writes only happen through the `write_note` / `write_reflection` MCP tools)
- **Obsidian (or any external editor) is an explicit, supported escape hatch.** Direct hand-edits are expected — for correcting a decayed confidence value, fixing a typo, or intervening when automation gets something wrong. This means Cognosis cannot assume it's the only writer in practice, even though `write_note`/`write_reflection` are the only *sanctioned* write path; §4.1.1 covers how out-of-band edits get reconciled.

#### 4.1.1 Reconciliation — detecting out-of-band edits
Because external edits (Obsidian, a text editor, `git` operations on the vault, etc.) bypass the MCP write path entirely, Postgres has no inherent way to know a file changed. Two mechanisms cover this, one live and one at boot:

- **Live detection:** a background file watcher (`fsnotify`) monitors `$XDG_DATA_HOME/cognosis/kb/` while the daemon is running. On a create/modify/delete event, Cognosis re-validates the file's frontmatter against the contract, then re-chunks, re-embeds, and upserts (or removes, on delete). Invalid frontmatter is logged as a sync error rather than indexed, so a broken hand-edit doesn't silently corrupt the index — it just doesn't get picked up until fixed.
- **Boot-time integrity check:** `fsnotify` events don't queue while the daemon is offline, so an edit made while Cognosis isn't running would otherwise go undetected indefinitely. On startup, Cognosis reconciles every file in the vault against its last known-good state before accepting MCP connections:
  - **Fast path:** compare each file's `mtime` + size against the values stored in Postgres from the last sync. Unchanged on both → skip, assume unmodified. This means a typical boot only inspects files that actually differ, not the whole vault.
  - **Hash path (drift confirmation / periodic safety net):** for files whose `mtime`/size differ (or on a periodic full sweep, since some editors don't reliably update `mtime`), hash the content with **BLAKE3** and compare against the stored hash. BLAKE3 is used over SHA-256 for both its raw per-file throughput (simpler round function, SIMD-friendly) and because hashing is trivially parallelizable across files via a worker pool — the mtime/size pre-check is what eliminates most of the work, BLAKE3 is what makes the remaining work fast.
  - Any confirmed drift triggers the same re-validate → re-chunk → re-embed → upsert path as the live watcher.

- **Write conflict handling:** if an MCP write (`write_note`) and an external edit to the same file race, Cognosis holds a lock on files it is actively writing; the file watcher ignores disk-change events for a file during that window. Outside of active-write windows, hand-edits apply normally. This is last-writer-wins in spirit but scoped narrowly enough (a single file, a short write window) that real collisions should be rare.
- **Sync is one-way, disk → Cognosis.** Cognosis does not push changes back into an open Obsidian vault; lifecycle updates (e.g. `last_reinforced` changing after a `compile_lifecycle` call) are visible next time the file is reloaded, same as any other external file change would be.

**Folder layout** — generalized from silo-kb's category-named folders (`daily/`, `deep-thoughts/`, `knowledge/{concepts,cursed-knowledge,lessons-learned,archive/faded}`, `projects/`) into folders that represent *processing stage / content shape* rather than semantic type. Semantic type moves to frontmatter instead, since `project` is already a frontmatter tag rather than a folder — folder-as-category would otherwise conflict with that:

```
$XDG_DATA_HOME/cognosis/kb/
├── index.md          (root index, generated)
├── log.md            (append-only knowledge log)
├── entries/          (raw, timestamped capture — replaces daily/)
├── notes/            (atomic processed notes — replaces knowledge/{concepts,cursed-knowledge,lessons-learned})
├── reflections/       (persona-authored freeform writing — replaces deep-thoughts/; see §5)
└── archive/           (retired notes — replaces knowledge/archive/faded)
```

Frontmatter carries the semantic dimensions the old folders used to encode:

```yaml
---
id: ...
project: silo-kb | cognosis | none
category: concept | cursed-knowledge | lesson-learned | reflection | entry
persona: deep-thoughts | ...      # only present when category: reflection
status: active | faded | archived
confidence: ...
maturity: ...
created: ...
updated: ...
---
```

Obsidian compatibility is unaffected — wiki-links and plain YAML frontmatter work the same regardless of folder depth, and Obsidian's Properties view / Dataview queries can filter or group by `category`, `project`, or `persona` the way the file-tree folders used to, so nothing is lost in terms of "browse by type," it just moves from folder navigation to a query view.

### 4.2 Postgres — managed, mostly derived
- **Managed by Cognosis**, not an externally-expected service (via the flake, for installation — see §7)
- Single schema, single database — matches silo-kb's `project`-column approach rather than per-project schemas
- `notes` table stores full note content, not just derived metadata

  This is a deliberate divergence from silo-kb's strict "DB is 100% disposable index" guarantee. Because Cognosis is the sole writer and writes are sequenced (file write → DB upsert), `notes.content` is a **fully rebuildable mirror** of the markdown files — recoverable by walking `$XDG_DATA_HOME/cognosis/kb/`, just not re-derived through parsing logic on every rebuild. This trades a small amount of the "purity" of silo-kb's model for meaningfully faster reindex and `get_note`/`query_knowledge` operations (no per-file disk reads).

- `chunks`, `links`, and all embedding tables remain **fully derived and droppable**, exactly as in silo-kb — rebuilt from `notes` + re-chunking + re-embedding.

### 4.3 Embeddings — one table per provider/model
pgvector requires a fixed dimension per column, and mixing embedding spaces from different models in a single indexed column breaks both correctness (vectors from different models aren't comparable) and HNSW's ability to search efficiently. So:

- Each embedding provider/model gets its **own table**: `embeddings_<provider>_<model_slug>`, each with its own `vector(N)` column and its own HNSW index
- A small metadata table, `embedding_providers` (name, model, dimension, table_name, created_at, active flag), tracks what's been provisioned and which provider is currently active
- **Provisioning is dynamic, not static-schema:** when a new provider/model is registered, Cognosis probes it once (embeds a throwaway string, measures the returned vector's length) to determine `N`, then runs a `CREATE TABLE` + `CREATE INDEX` at runtime. A small lookup table of known dimensions (e.g. nomic-embed-text = 768) can shortcut this for recognized models, but live-probing is what handles genuinely unknown models.
- Switching the active provider means running a re-embed pass into the new table; old tables can be kept (for rollback/comparison) or dropped later. This is unavoidable regardless of schema — embeddings from different models are never directly comparable.
- Local (Ollama) vs. remote embedding provider is a **config-only setting** — no runtime toggle/slash-command. Changing it requires an explicit config edit (and a restart or config-reload), by design.

#### 4.3.1 Migration — hybrid, zero-downtime
A naive re-embed pass would be a monolithic, blocking migration: with tens of thousands of chunks, a remote provider hits rate limits immediately and a local (Ollama) pass can max out the GPU for hours — and for the entire duration, the new table is incomplete, so the agent is effectively blind if queries only look at the new table. Cognosis avoids this with a **hybrid migration strategy** that keeps the system fully queryable throughout:

- **Query-time fallback view.** Retrieval reads through a view (or equivalent query logic) that prefers the new provider's table but falls back to the old table for any chunk not yet migrated: `COALESCE(new.embedding, old.embedding)`. This is what makes the migration invisible to the agent — there is never a state where a chunk has *no* embedding to search against.
- **Path 1 — background back-fill.** A worker (goroutine) runs continuously while a migration is `in_progress`: pull chunks missing from the new table, batch them (e.g. 32 at a time), embed via the new provider, upsert. Pausable/resumable (`--pause` / `--resume`), rate-limit aware, and this is what guarantees eventual 100% coverage — the precondition for flipping `active` and, later, retiring the old table.
- **Path 2 — lazy, on-demand migration.** Independently of the back-fill worker, a chunk gets migrated the moment it's *touched*: a `query_knowledge` hit that resolves to an old-table chunk triggers an async embed-and-upsert for that chunk after the response is served (non-blocking); a `write_note` update to a pre-migration chunk embeds it into the new table as part of that same write. This means actively-used ("hot") memory migrates ahead of the back-fill's batch order, and compute isn't spent re-embedding chunks nobody queries.
- **The two paths are naturally idempotent** — both are just "ensure this chunk exists in the new table," upsert-on-conflict. Whichever gets there first wins; the back-fill worker's own selection query (`chunks not yet in the new table`) already excludes anything lazy migration already handled, so there's no wasted duplicate work.
- **New writes during migration** always embed via both the active and the in-progress provider, so freshly-written notes never depend on either migration path to be fully covered.
- **Completion:** when back-fill coverage reaches 100%, `migration_status` flips to `complete` and `active` switches to the new provider. Old tables are **not** auto-dropped — see §8, pruning stays a deliberate, explicit action.
- **Observability:** a `migration_state` table (from_provider, to_provider, started_at, chunks_total, chunks_done, chunks_migrated_backfill, chunks_migrated_lazy, chunks_failed, paused, last_error) backs both a CLI status command and an MCP tool so Claude can check progress/ETA and decide, e.g., whether to pause a migration before running a large batch of writes.
- **Rollback:** flipping `active` back to the old provider is always available and immediate (the fallback view means the old table is never removed mid-migration); the half-migrated new table is left in place so a paused/failed migration can be resumed later rather than restarted from scratch.

### 4.4 Schema Migrations

Distinct from the embedding-provider migration in §4.3.1 (that one moves *data* between embedding tables at runtime). This one versions the *shape* of `notes`/`chunks`/`links`/`embedding_providers` etc. across Cognosis releases.

- Tooling: `golang-migrate`, with SQL migration files embedded via `go:embed` so the binary stays self-contained — no separate migrations directory to ship or lose track of.
- A `schema_migrations` table tracks applied versions.
- Migrations auto-apply on startup, before the daemon accepts MCP connections — the same "reconcile state before serving" pattern already used for the boot-time integrity check in §4.1.1.
- `cognosis schema status` (CLI) reports the current schema version and any pending migrations, mirroring `cognosis embeddings status` (§4.3.1, §9).
- Down-migrations exist for development convenience but aren't treated as a guaranteed-safe rollback path once real data exists under a released version — the safety net is a reminder to `pg_dump` before migrating, not a robust down-migration guarantee.

### 4.5 Concurrency & Locking

Cognosis is designed to serve multiple simultaneous MCP *clients* (§11), but as a **single daemon instance** — these are different axes and only the first is actually supported:

- **Per-path in-memory lock.** `write_note` / `write_reflection` take a lock keyed on the note's path for the duration of the write. A second client writing the same path blocks briefly rather than racing; the file watcher already respects this lock (§4.1.1), so no new mechanism is introduced — only the existing lock's scope widens to cover client-vs-client, not just watcher-vs-client.
- **Postgres advisory lock for whole-KB operations.** `compile_lifecycle` and starting/pausing/resuming an embedding migration take a named advisory lock (`pg_advisory_lock`) for the operation's duration. A concurrent second call gets an explicit "operation already in progress" error rather than racing against shared state — this matters more once §11 makes multi-user, multi-client access the expected case rather than the exception.
- **Reads are unaffected.** `query_knowledge`, `list_notes`, `get_note`, `list_personas`, `get_persona` are read-only and rely on normal Postgres transaction isolation — no additional locking needed.

**Single active daemon instance is an invariant, not an implementation detail.** The in-memory path lock only serializes writes *within one process*; it provides no protection if two Cognosis processes point at the same Postgres database. Running two instances concurrently is unsupported and can corrupt the vault (two processes racing on the same file, or two boot-time reconciliation passes overlapping). To make this failure mode explicit rather than latent:

- A **boot-time lock file** (`$XDG_STATE_HOME/cognosis/daemon.lock`, holding the running PID) is acquired on startup and held for the daemon's lifetime. A second `cognosis start` on the same machine detects the held lock and refuses to start, rather than starting anyway and racing.
- This does **not** cover two instances on two different machines pointed at the same remote Postgres (§11) — that scenario has no guard yet and is called out as unsupported rather than silently broken.
- **If clustering/HA is ever required**, this section's locking model doesn't extend — it would need distributed locks (e.g. Redis or Postgres `LISTEN`/`NOTIFY`-backed coordination) in place of in-memory locks, and the fsnotify-per-instance model would need to become a single designated watcher rather than N racing watchers. That's a different architecture, deferred (§0.1) rather than designed here.

### 4.6 Deletion — Soft vs. Hard

Two genuinely separate paths, not one mechanism doing double duty:

- **Soft-delete (default, everyday path).** `compile_lifecycle` moves a note to `status: archived`. The file, its DB row, its chunks, and its embeddings all remain in place — archived notes are excluded from default retrieval but stay fully present and recoverable. This is the only deletion path exposed as an MCP tool; Claude can archive but not destroy.
- **Hard-delete (rare, deliberate, CLI-only).** `cognosis note delete <path> --hard` removes the markdown file and cascades a purge across `notes`, `chunks`, every embedding table, and `links` — genuine erasure, for cases (e.g. a GDPR-style request) where "excluded from retrieval" isn't sufficient and the content must stop existing anywhere in the system. Requires interactive confirmation (or `--yes` for scripting), since — unlike everything else in Cognosis — it isn't something a re-index or rollback can undo.
- Re-indexing alone does not satisfy a hard-delete requirement: rebuilding `chunks`/embeddings from `notes.content` (or from the file) only reproduces what soft-delete already leaves in place. What makes hard-delete different is removing the source content itself, not just its derived index entries.

## 5. Personas & Reflections

silo-kb's `deep-thoughts` feature is more than a category — it's a full generative persona (a voice/style guide, a structural formula, and a pre-write quality checklist, as in `prompts/deep-thoughts-persona.md`). Cognosis generalizes this into a small persona subsystem rather than a single hardcoded feature:

- **Personas live as files**, expanded from silo-kb's single `prompts/deep-thoughts-persona.md` into `prompts/personas/` — one self-contained file per persona (voice guide + structure + quality checklist, same shape as the existing deep-thoughts file). Adding a new persona later is just adding a new file; no code changes needed on the content side.
- **Toggling is enable/disable only**, per persona, via the config registry (see §9). No frequency/probability scheduling, no automatic session-end trigger — a disabled persona simply isn't available to be invoked; its file stays in place for later reactivation.
- **Selection is contextual and Claude-driven.** When multiple personas are enabled, there is no random pick and no fixed rotation — Claude reads the session and judges whether a reflection is warranted at all, and if so, which enabled persona fits the moment.
- **The trigger is an explicit MCP tool call**, not a scheduled hook — consistent with the same explicit-over-automatic pattern already used for `compile_lifecycle` and the embedding provider switch. Claude decides in the moment and calls the tool; Cognosis does not decide on Claude's behalf.
- **Discovery is two-tier, to protect the context window.** Returning every enabled persona's full file content (voice guide, structural formula, checklist) on every discovery call is wasteful token bloat and actively counterproductive — it dilutes the model's attention with instructions it doesn't need just to decide *whether* any persona fits, and cost/risk scales with however many personas are enabled.
  - **Tier 1 — `list_personas()`:** returns only lightweight metadata per enabled persona — `id`, `name`, and a one-sentence `description`/semantic tag (a few dozen tokens each, sourced from that persona's own frontmatter so it stays in sync with the file rather than living in a separate registry). Enough for Claude to judge fit without paying for full content it may not use.
  - **Tier 2 — `get_persona(id)`:** fetches the full persona file (voice guide, structure, checklist) for exactly the one persona Claude has decided fits the moment. Only called when Claude is actually about to write in that voice.
  - Cognosis's own role stays narrow throughout: validate the `persona` argument against the enabled list, write the note into `reflections/` with correct frontmatter, and nothing more. All persona judgment is Claude's.

### 5.4 Personas as Retrieval Filters

Personas so far shape *how* a reflection is written (§5). Extending them to also shape *what's retrieved* turns a persona into an optional retrieval lens, not only a writing voice:

- `query_knowledge` (§6) gains an optional `persona_filter` parameter. When set, retrieval biases toward notes a persona's own frontmatter declares relevant — e.g. a `cursed-knowledge`-flavored persona weighting `category: cursed-knowledge` and low-confidence/disputed notes higher, even where they'd rank lower on pure semantic similarity.
- This is a weighting bias, not a hard filter — RRF fusion (§6) still runs across the full corpus; `persona_filter` adjusts fusion inputs rather than excluding non-matching notes outright. Existing `query_knowledge` semantics (`include_falsified`, `project` scoping) are unchanged.
- The bias mapping (which categories/tags a persona weights, and by how much) lives in that persona's own frontmatter, next to its voice guide — consistent with §5's "personas are self-contained files" principle. No separate registry to keep in sync.

### 5.5 Persona Chains

A reflection written in one persona's voice can set up context that makes a *different* persona's reflection more likely on a later pass:

- `write_reflection` can optionally set a `chain_hint` tag (e.g. `lesson-learned`) in a note's frontmatter — a signal, not a command, that this note is a natural jumping-off point for a follow-on reflection in a different voice.
- Chaining stays Claude-driven, not automatic — consistent with §5's "no automatic session-end trigger" principle. `list_personas()`'s lightweight metadata can include a `responds_to` hint (which `chain_hint` values that persona is designed to follow up on), so when Claude is judging whether a reflection is warranted, a recent note with a matching `chain_hint` is a strong signal to weigh — but Cognosis never invokes a chained reflection on its own.
- No new MCP tool is required: this is a frontmatter convention plus a metadata hint, layered onto the existing `write_reflection` / `list_personas` / `get_persona` surface.

## 6. MCP Tool Surface (draft)

**Write**
- `write_note(path, content, project)` — validates frontmatter against the contract, chunks, embeds (using the active provider), upserts note + chunks + links atomically relative to the file write
- `write_reflection(persona, content, project?)` — writes a persona-authored note into `reflections/` with `category: reflection` and the given `persona` tag; validates `persona` against the enabled persona list

**Query**
- `query_knowledge(text, project?, top_k, include_falsified?, persona_filter?, as_of?)` — hybrid retrieval (vector + FTS + graph, fused via RRF). `persona_filter` biases fusion toward a persona's relevant categories/tags (§5.4); `as_of` reasons over frontmatter timestamps to answer "what did the KB believe at time T" (§12). Both optional and additive — omitting them preserves current behavior. When more than one embedding provider is provisioned (e.g. mid-migration, or deliberately kept side-by-side), retrieval fans out across all provisioned providers' tables and fuses the combined results (§12).
- `list_notes(project?)` — introspection, browse without full content
- `get_note(path)` — introspection, full content + frontmatter for a single note
- `list_decaying(threshold_days, project?)` — surfaces notes whose `last_reinforced` is approaching staleness under existing decay rules; visibility only, not a new decay mechanism (§12)
- `list_personas()` — lightweight discovery: returns each enabled persona's `id`, `name`, one-sentence `description`, and (if set) `responds_to` chain hints (§5.5). Cheap enough to call just to check what's available.
- `get_persona(id)` — full-content fetch for exactly one persona (voice guide, structure, checklist), called only once Claude has decided that persona fits the moment.

**Lifecycle**
- `compile_lifecycle(reinforce: [], falsify: {}, dispute: {}, graduate: {}, dry_run: bool)` — explicit, Claude-driven. No automatic timer-based compilation; Claude constructs the operation set and calls this deliberately.
- `get_migration_status()` — returns the current embedding migration's progress (from/to provider, chunks done/total, split between lazy and back-fill, ETA, paused state, last error), if a migration is in progress; see §4.3.1.

## 7. Installation & Lifecycle Management

- **`flake.nix` is scoped to installation only** — pinning Postgres, pgvector, Ollama, and the Go toolchain, and producing the Cognosis binary, for systems with the Nix package manager. It does **not** manage starting/stopping the running daemon.
- **Service lifecycle management: both.** Cognosis supports self-managed lifecycle as the baseline (`cognosis start` / `stop` / `status`, self-daemonizing with a pidfile) so it works identically on any platform without depending on an OS service manager. Platform-native service definitions (a systemd user unit on Linux, a launchd plist on macOS) are provided as an *optional* install path for anyone who wants boot-time startup and native process supervision. The self-managed CLI remains the source of truth either way — the native service files simply invoke it, rather than duplicating lifecycle logic in two places.

### 7.1 Startup Dependency Checks

`cognosis start` does not manage Postgres or Ollama's process lifecycle — both are expected to already be running, provisioned separately (via `flake.nix`-installed systemd services, Docker, or manual start). What `cognosis start` does own is verifying they're reachable before the daemon reports itself as up, and **failing fatally, not degrading, if they aren't**:

- **Postgres:** attempt a connection using the configured DSN (`$XDG_CONFIG_HOME/cognosis/config.yaml`, §9.2). On failure, exit nonzero with a clear error identifying which check failed and the attempted connection target. No retry-loop in the foreground — a supervisor (systemd) owns restart policy, not the binary itself.
- **Ollama (or configured remote embedding provider):** a lightweight reachability check (e.g. hit its version/health endpoint) — not a full embed-and-verify round trip, since that's slower and unnecessary just to confirm the service is listening. On failure, the daemon does **not** start at all — no degraded mode where read-only tools (`list_notes`, `get_note`) work while `write_note`/`query_knowledge` fail individually. The rationale: a partially-working daemon is a worse failure mode to debug than one that refuses to start, and it's consistent with §16's "context-less session that looks normal is worse than one that visibly fails" principle — here, a Cognosis that looks up but silently can't embed is the same shape of hazard.
- **Schema migrations (§4.4)** already run at this point in the sequence (auto-apply before accepting MCP connections) — the checks above happen first, since a migration attempt against an unreachable Postgres is a confusing error to debug compared to a clear "Postgres unreachable" message.
- **Startup order:** Postgres reachability → schema migrations (§4.4) → boot-time vault reconciliation (§4.1.1) → Ollama/embedding-provider reachability → daemon lock acquisition (§4.5) → MCP server accepts connections. Any failure in the first four steps is fatal; the daemon does not proceed past it in a partial state.
- **`cognosis status`** surfaces the same checks (Postgres, embedding provider, schema currency) as part of its normal output, not just at startup — so `status` answers "is this actually healthy" rather than only "is the process alive."

## 8. Open Questions / Deferred Decisions

- Rate limiting per token, once remote multi-user access (§11) sees real concurrent load
- Exact backfill order/prioritization heuristics for `list_decaying` (§12) beyond a flat threshold

**Resolved:**
- **Service lifecycle management** — both self-managed CLI and optional platform-native service files; see §7.
- **`embedding_providers` migration/versioning** — resolved as the hybrid background-backfill + lazy on-demand strategy; see §4.3.1.
- **Old embedding tables — pruned manually.** After a provider switch, the previous provider's table is left in place rather than auto-dropped. Cleanup is a deliberate, explicit action (`cognosis embeddings prune <provider>`), not something that happens automatically as a side effect of switching. This preserves the ability to roll back or compare providers side-by-side until the operator is confident the switch is final. CLI surface: `cognosis embeddings prune <provider>` (§9).
- **Out-of-band edit reconciliation (Obsidian, etc.)** — resolved as live `fsnotify` watching plus a boot-time `mtime`/size pre-check backed by BLAKE3 hashing for confirmed drift; see §4.1.1.
- **Persona discovery token cost** — resolved as a two-tier `list_personas()` / `get_persona(id)` split; see §5 and §6.
- **Auth model for remote/non-Claude MCP clients** — resolved as per-client bearer tokens with audit logging; see §11.
- **Schema versioning across releases** — resolved as `golang-migrate` with embedded SQL, auto-applied at boot; see §4.4.
- **Concurrent MCP clients** — resolved as path-scoped in-memory locks plus Postgres advisory locks for whole-KB operations; see §4.5.
- **GDPR-style erasure** — resolved as a separate CLI-only hard-delete path, distinct from the MCP-exposed soft-delete (archival); see §4.6.

## 9. CLI & Configuration Infrastructure

Cognosis uses **Cobra** for command structure and **Viper** for configuration, the standard pairing for this kind of Go daemon/CLI hybrid — Cobra owns the command surface, Viper owns config loading, binding, and persistence, and they compose cleanly rather than overlapping.

- **Config file**, `$XDG_CONFIG_HOME/cognosis/config.yaml` (Viper-managed; §9.2), holds settings that were previously described as "config-only, no runtime toggle" — the active embedding provider and registered provider list (§4.3), and the **enabled persona registry** (§5): each entry's `id`, `name`, and `description`, kept alongside — not instead of — the persona files themselves, so `list_personas()` can read the lightweight registry without touching full file content, while `get_persona(id)` still resolves to the actual file in `prompts/personas/`.
- **Cobra provides the command tree**, grouped by resource/noun rather than a flat list of verb-prefixed commands:
  - `cognosis start` / `stop` / `status` — daemon lifecycle (§7); kept top-level since these are the primary entrypoint, not a resource category.
  - `cognosis embeddings migrate --from --to [--pause|--resume|--dry-run]` (§4.3.1), `cognosis embeddings prune <provider>` (§8), `cognosis embeddings status` — progress/ETA for an in-progress migration.
  - `cognosis schema status` — pending/applied schema migrations (§4.4).
  - `cognosis note delete <path> --hard [--yes]` — the deliberate, irreversible erasure path (§4.6); soft-delete stays MCP-only via `compile_lifecycle`, so there's no `note archive` CLI equivalent.
  - `cognosis token create <name>`, `cognosis token revoke <name>`, `cognosis token list` — bearer-token management for remote/multi-client access (§11).
  - `cognosis config get <key>` / `cognosis config set <key>` — reading or persisting individual config values without hand-editing YAML.
  - `cognosis context inject --project <p> --budget N` — truncated index for Claude Code's SessionStart hook (§16); the only CLI command meant to be called by a hook rather than a human.
  - This grouping (`<noun> <verb>`) is the convention going forward for anything added later — new resource categories get their own noun rather than growing the top-level command list.
- **Env var overrides** are supported automatically via Viper's env-binding (e.g. for secrets like a remote embedding provider's API key), so secrets don't need to live in the config file in plaintext.
- This section covers structure only; exact flag sets and command implementations are left to the implementation phase, not fixed in this draft.

### 9.2 XDG Base Directory Compliance

Cognosis resolves all filesystem paths through the **XDG Base Directory Specification** rather than a single hardcoded `~/.cognosis/` directory, since config, data, state, and cache are genuinely different categories with different backup/wipe/sync expectations:

| XDG var | Default (Linux) | Cognosis uses it for |
|---|---|---|
| `XDG_CONFIG_HOME` | `~/.config` | `cognosis/config.yaml` — the one file a human hand-edits (§9) |
| `XDG_DATA_HOME` | `~/.local/share` | `cognosis/kb/` — the markdown vault, source of truth (§4.1) |
| `XDG_STATE_HOME` | `~/.local/state` | `cognosis/daemon.lock` (§4.5); file logs, if file-logging is ever enabled instead of stdout/journald |
| `XDG_CACHE_HOME` | `~/.cache` | Reserved, unused in v1 — nothing in the current design is regenerable-on-loss cache; held for future use (e.g. a local embedding-response cache) rather than designed against now |

- **Library: `adrg/xdg`.** Go's stdlib (`os.UserConfigDir()`, `os.UserCacheDir()`) only covers two of the four directories and only follows the XDG spec on Linux — no data/state equivalents, and no macOS/Windows path translation. `adrg/xdg` covers all four vars plus native-path fallbacks on macOS (`~/Library/Application Support`, etc.) and Windows (`%APPDATA%`, `%LOCALAPPDATA%`). This is a justified dependency, not a default reach-for-a-package choice: the alternative is hand-rolling four platform-conditional path functions, which is exactly the kind of cross-platform detail that's easy to get subtly wrong (e.g. Windows has no real `XDG_STATE_HOME` analog; `adrg/xdg` has already made that call).
- **Precedence is env-var-first, package-default-second, nothing else.** If `$XDG_DATA_HOME` (etc.) is set, it wins; otherwise `adrg/xdg`'s platform-appropriate default applies. No third fallback tier, and no auto-detection/migration of a pre-existing `~/.cognosis/` — nothing has shipped yet, so there's no real installed base to migrate, and adding migration logic for a directory layout that never had a release would be solving a problem that doesn't exist.
- **All four directories are created (`mkdir -p`-equivalent) on first daemon start**, under `<xdg-dir>/cognosis/`, not assumed to pre-exist.
- Every hardcoded `~/.cognosis/*` path elsewhere in this document (§3, §4.1, §4.1.1, §4.5, §9) is superseded by this section; read them as `$XDG_DATA_HOME/cognosis/...`, `$XDG_STATE_HOME/cognosis/...`, or `$XDG_CONFIG_HOME/cognosis/...` per the table above.

## 10. Build Tooling

Cognosis is implemented in **Go**, using **Mage** as the task runner (build, test, lint, codegen, and any future steps like proto generation, all defined as Go functions rather than a separate DSL/Makefile syntax — keeps the build tooling in the same language as the project itself).

`flake.nix` (§7) handles environment/dependency provisioning (Postgres, pgvector, Ollama, Go toolchain); Mage handles the actual build/dev workflow on top of that environment (e.g. `mage build`, `mage test`, `mage install`). The two are complementary, not overlapping: Nix answers "what's installed," Mage answers "what do I run."

### 10.1 Go Language Conventions

Three conventions apply project-wide, beyond what a build-tooling choice alone implies:

- **Generics over interfaces, where both would work.** Where a design choice comes down to `interface{}`/`any` plus a type assertion versus a generic function/type, prefer the generic — it moves a class of errors (wrong-type-at-runtime) to compile time. This is not a blanket ban on interfaces: interfaces remain the right tool for *behavioral* contracts (something implements `Read`/`Write`/`Close`), where the point is polymorphism over behavior, not over a stored type. Generics are for *structural* contracts — a `Store[T]`, a `Cache[K, V]`, a chunk-batch processor generic over embedding-provider response types — where the alternative is `any` plus a runtime type assertion that a generic parameter would have caught at build time. Concretely in this codebase: the embedding-provider table abstraction (§4.3) and the RRF fusion merge step (§6) are generic-shaped problems; the MCP tool dispatch surface (§6) is an interface-shaped problem (different tools, common `Call(ctx, args) (result, error)` behavior). Don't force one shape into the other's slot.
- **`context.Context` as the first parameter, threaded through every call that crosses an I/O or goroutine boundary.** Postgres queries, HTTP/MCP handlers, the fsnotify watcher's event loop, the embedding-migration background worker (§4.3.1) — all take and respect `ctx` for cancellation and deadlines. This is what makes the 2s SessionStart timeout (§16) and the migration's pause/resume (§4.3.1) actually enforceable rather than aspirational: a goroutine that ignores its context can't be cancelled, full stop. Concretely: no `context.Background()` calls below the top of a request/command; it's threaded down, not manufactured mid-stack.
- **Structured logging, not `fmt.Println`/`log.Printf`.** One logger (`log/slog`, stdlib since Go 1.21 — no dependency to justify) used throughout, with consistent fields: `component` (e.g. `mcpserver`, `store`, `embed`), and request-scoped fields (`token_id`, `project`, `tool_name`) attached via context where available. This matters concretely for two things already in the design: the audit log (§11) needs `tool_name`/`project`/`token_id` correlated per call, and boot-time reconciliation (§4.1.1) needs to report *which* files drifted and why without grep-ing free-text log lines. Structured fields make both queryable; string-formatted logs don't.

## 11. Authentication & Access Control

Originally deferred (§8). Since Cognosis is now expected to run on a remote host serving multiple clients/users, not just localhost, auth is a first-class section rather than a config afterthought:

- **Per-client bearer tokens**, not a single shared secret. `cognosis token create <name>` generates a token, prints it once, and stores only its hash (Argon2id) in a `tokens` table (`id`, `name`, `token_hash`, `created_at`, `revoked_at`, `last_used_at`). Clients send `Authorization: Bearer <token>`; middleware resolves the token to an identity and attaches it to the request context before it reaches any MCP tool handler.
- **Revocation is immediate and live** — checked against the `tokens` table on every request, not a long-lived cache — so `cognosis token revoke <name>` takes effect on the next request, not on next restart.

  This trades request latency (a synchronous token-table lookup on every call) for correctness (no revoked-token window). At the request volumes a single-user or small-team deployment sees, this is undetectable. At higher concurrency (rough threshold: 100+ concurrent agents, ~10k requests/min) it becomes a measurable bottleneck, and the fix at that point is a short-lived in-memory cache (TTL 5–15s) invalidated via Postgres `LISTEN`/`NOTIFY` on revoke — cached for normal reads, still bounded to a single-digit-second window after a revoke. This is deferred until real load justifies it; the synchronous path is the correct default until then.
- **Every tool call is audit-logged.** An `audit_log` table (`id`, `token_id`, `tool_name`, `project`, `args_summary`, `timestamp`, `success`, `error`) records who called what, when, on which project. `args_summary` is a redacted/truncated form (tool name plus key identifying args like `path` or `project`, not full note content) — enough to answer "who touched this note and when" without duplicating note content into a second table.
- **Transport is TLS, not plaintext, once remote.** The documented path is Streamable HTTP behind a reverse proxy (Caddy/nginx) that terminates TLS and forwards to Cognosis on loopback — simplest cert management, and keeps Cognosis itself a single-purpose daemon rather than a TLS-terminating web server. Direct built-in TLS (cert/key paths in config) remains a fallback for setups without a reverse proxy.
- **Local-only deployments keep the old low-friction default** — bind to loopback, single auto-generated token, no reverse proxy required. Remote deployment is opt-in (a config flag plus a bind-address change), not the default posture.
- `cognosis token create <name>` / `cognosis token revoke <name>` / `cognosis token list` round out the CLI surface (§9). `get_audit_log` is deliberately **not** exposed as an MCP tool — audit review is an operator/CLI concern, not something an MCP client should be able to query about itself or other clients.

## 12. Retrieval Extensions

Several ideas that extend `query_knowledge` (§6) beyond straight hybrid RRF, without changing its core contract:

- **Cross-model retrieval.** Where §4.3 requires exactly one *active* embedding provider for writes, `query_knowledge` can optionally fan a query out across more than one provider's table when more than one happens to be provisioned (e.g. a general-purpose embedding space kept alongside a code-tuned one), fusing results from both into the same RRF pass. Read-only and additive — it doesn't change what gets written, only what a query can search against.
- **Temporal queries.** `query_knowledge` gains an optional `as_of` timestamp parameter (§6). When set, retrieval reasons over each note's frontmatter timestamps (`created`, `updated`, `falsified_at`) to answer "what did the KB believe at time T" rather than "what does it believe now" — useful for reconstructing why a past decision made sense given what was known then.
- **Confidence decay surfacing.** `list_decaying(threshold_days, project?)` (§6) surfaces notes whose `last_reinforced` is approaching staleness under the existing decay rules — not a new decay mechanism, just visibility into ones already drifting toward it, so Claude (or a human) can decide to reinforce, dispute, or let them fade via the existing `compile_lifecycle` tool.
- **Vectorized frontmatter.** Frontmatter fields like `confidence`/`maturity`/`category` get their own lightweight embedding, separate from body content, enabling structural queries ("show me all developing notes") to run as a vector search rather than a SQL filter — mainly useful once frontmatter values become free-text-ish (e.g. natural-language confidence notes) rather than a fixed enum.
- **Generative summaries.** `write_note`/`write_reflection` can optionally compute and cache a one-line LLM-generated summary at write time, stored as a `summary` column on `notes`. `query_knowledge` results return this cached summary alongside each hit as a cheap first-pass filter, and it can also serve as a fast pre-ranking signal before full RRF fusion runs on the top candidates.

## 13. Cross-Project Knowledge Graph

Extends the link graph (§2, §6) past a single project's notes:

- **Cross-project wikilinks.** A note's links can reference another project's note using a `project:note-id` syntax, so the link graph spans projects rather than stopping at the `project` frontmatter boundary. This turns "silo-kb taught us X, which shaped Cognosis's design of Y" into a traversable edge instead of a fact living only in a human's memory.
- **Project-local aliases.** A note can carry an `aliases` map (`{ project: term }`) so the same underlying concept surfaces under each project's own vocabulary. `query_knowledge`'s `project` scoping (§6) already exists; aliasing means a query phrased in one project's terms can still resolve to a concept authored under another project's terminology, without requiring duplicate notes.
- Both are additive frontmatter/link conventions — no schema change beyond what `links` (§2) and existing frontmatter already support structurally.

## 14. Lifecycle Automation Extensions

- **Retrieval-augmented compilation.** `compile_lifecycle` (§6) can optionally run a `query_knowledge` pass internally before finalizing a `falsify`/`dispute`/`graduate` operation set — e.g. checking whether recent notes contradict a note flagged for archival before committing to archive it. This stays inside the existing explicit, Claude-driven trigger model (§5's "explicit tool call, not a scheduled hook" principle extends here too) — it's `compile_lifecycle` reasoning more carefully about the operations it's about to perform, not a new automatic trigger.

## 15. External Integrations

- **Slack/Discord sync.** An optional bridge process (separate from the core daemon) posts persona reflections as thread replies and pulls ephemeral chat captures into `entries/` via the existing `write_note` MCP path — Cognosis's own role stays exactly "write notes, run retrieval"; the bridge owns platform-specific API concerns.
- **Git commit integration.** An optional, opt-in hook (not automatic by default, consistent with the "explicit over automatic" pattern used throughout this document) that logs commits as `entries/` notes with structured frontmatter linking to related notes — passive citation-refresh, but only for repos that explicitly enable it.
- Both integrations are deliberately kept outside Cognosis's core process boundary — they're consumers of the MCP write path, not new server responsibilities, keeping the daemon's own scope (§1) unchanged.

## 16. Context Injection & Client Hooks

silo-kb's automatic session behavior — SessionStart index injection, PreToolUse frontmatter validation, a SessionEnd sweep — is implemented as Claude Code hooks: local shell scripts wired via `.claude/settings.json`, calling the local `silo-kb` binary directly against a co-located vault and Postgres. Cognosis's centralized, potentially-remote daemon model (§11) changes *what* each hook does, but the hook mechanism itself is still required for anything that must happen automatically, before the agent gets a turn — MCP tools are agent-invoked by design (§5's "explicit tool call, not a scheduled hook" principle), and `SessionStart` fires before the agent has had any chance to decide something. Hooks and MCP tools are complementary here, not competing: hooks cover the handful of moments that are inherently pre-agent or deterministic; MCP tools cover everything the agent chooses to do.

- **SessionStart — index injection, same shape, different transport.** `cognosis context inject --project <p> --budget N` replaces `silo-kb inject-index`. Rather than reading `$XDG_DATA_HOME/cognosis/kb/` directly — which the CLI *could* do locally, but would skip the daemon's derived index and any in-flight lifecycle state — it authenticates with its configured token (§11) and calls the daemon, local or remote, for a truncated index scoped to `<p>`, then prints the result to stdout for the hook to inject. The daemon does the actual generation/truncation (the Cognosis analog of silo-kb's `indexgen`); the CLI is just the transport. `<p>` is resolved from a `.cognosis-project` marker file at the repo root — a one-line project tag — rather than a global path-to-project registry, so cloning a repo to a new machine or path needs no config update.

  **Failure mode is explicit: fail loud, block session start.** If the daemon is unreachable — connection refused, or no response within a 2s timeout — the hook exits nonzero rather than silently injecting an empty index. Claude Code will not proceed with the session without context. This is a deliberate choice, consistent with the "fail loudly at boundaries" principle: a context-less session that looks normal is worse than a session that visibly fails to start, because the former hides the fact that the agent is operating without its memory. For local deployments this is rare in practice (the daemon self-daemonizes and is expected to auto-start on login/boot); for remote deployments (§11), the reverse proxy's own health checks mean the failure surfaces to the operator quickly rather than only on the next session attempt.
- **PreToolUse — no longer needed as a separate hook.** silo-kb's validator intercepts a raw `Write`/`Edit` because the agent has direct filesystem access to the vault; Cognosis removes that access entirely — `write_note`/`write_reflection` (§4.1) are the only write path, and frontmatter validation already runs synchronously inside those handlers before anything is written. There's no unguarded `Write`/`Edit` call on vault files left to intercept, so the PreToolUse hook silo-kb needs simply has no Cognosis equivalent to build. (Out-of-band edits — Obsidian, a text editor — are still covered, but by the fsnotify/boot-reconciliation path in §4.1.1, not a hook.)
- **SessionEnd — kept, but as a nudge rather than a scraper.** silo-kb's `session-end-extract.sh` headlessly re-invokes Claude to pattern-match the transcript for missed categorized entries and write them directly to the daily log — a completeness backstop for content the agent noticed but didn't get around to writing. That problem (agent runs out of session before writing durable material) exists independent of architecture, so the backstop is worth keeping — but Cognosis reframes it: rather than a script parsing the transcript and writing on the agent's behalf, the SessionEnd hook triggers one last headless turn that reminds the agent to call `write_note`/`write_reflection` itself if anything from the session is durable. This keeps judgment about what's worth keeping on Claude's side (the same principle behind persona invocation in §5), rather than a regex deciding what counts as durable.
- **Hooks are always local, even when the daemon isn't.** Claude Code hooks execute as local shell commands, so a fully remote Cognosis deployment (§11) still needs a thin `cognosis` CLI installed locally as the hook's entry point — the hook shells out to the local binary, which makes the authenticated network call. This is the same pattern §9's CLI already uses for everything else; hooks don't introduce a new client, they're just another caller of the existing CLI surface.

## 17. Naming

**Cognosis** — a blend of *cognition* + *gnosis* (Greek: knowledge, or knowing through direct experience/reasoning). Landed here after an initial choice of "Engram" was reconsidered: that name is heavily used already, most notably by DeepSeek's [Engram](https://github.com/deepseek-ai/Engram) (a conditional-memory module for LLMs, ~4.4k stars) — a close enough collision, in the same general space, to cause real confusion. Cognosis has no meaningful prior use in this space.

The name also fits the system better than a pure "memory" word would: this isn't just a store of facts, it's a system that reasons about the state of its own knowledge — reinforcing, disputing, decaying, and graduating notes over time. "Knowing," not just "remembering," is the more accurate frame.
