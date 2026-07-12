# Cognosis — Implementation Guide

**Companion to:** `cognosis-architecture-draft.md` (the design authority; this guide sequences it, it does not restate it)
**Status:** Pre-implementation plan
**Scope:** v1 per §0.1 — everything except the Slack/Discord bridge (§15)

Each phase below states a **hypothesis** (what we believe will work), **deliverables**, and **exit criteria** (the evidence that proves the phase done). Per the epistemic contract: exit criteria are artifacts — passing tests, reproducible commands — not assertions. A phase whose exit criteria can't be demonstrated isn't done.

---

## Repository Layout

```
cognosis/
├── flake.nix                      # Postgres+pgvector, Ollama, Go toolchain, adrg/xdg noted (§7)
├── magefile.go                    # build / test / lint / install targets (§10)
├── cmd/
│   └── cognosis/main.go           # thin entrypoint; wiring only
├── internal/
│   ├── cli/                       # Cobra command tree (§9)
│   ├── config/                    # Viper loading, XDG path resolution via adrg/xdg (§9.2)
│   ├── daemon/                    # lifecycle: startup checks (§7.1), lock file (§4.5), shutdown
│   ├── store/                     # Postgres access; schema migrations via golang-migrate (§4.4)
│   ├── vault/                     # markdown vault: frontmatter contract, read/write, folder layout (§4.1); history repo commits/restore (§4.7)
│   ├── watch/                     # fsnotify watcher + boot reconciliation, BLAKE3 (§4.1.1)
│   ├── chunk/                     # chunking (ported mechanics from silo-kb)
│   ├── embed/                     # provider interface, Ollama client, dynamic table provisioning (§4.3)
│   ├── query/                     # RRF fusion: vector + FTS + graph (§6)
│   ├── lifecycle/                 # compile_lifecycle state machine (§6)
│   ├── persona/                   # two-tier discovery, reflections (§5)
│   ├── auth/                      # bearer tokens, Argon2id, audit log (§11)
│   ├── mcpserver/                 # MCP Streamable HTTP server, tool dispatch (§3, §6)
│   └── migrate/                   # embedding migration: backfill worker + lazy path (§4.3.1) [Phase 5]
└── migrations/                    # SQL files, go:embed'd (§4.4)
```

Conventions in force throughout (§10.1 + ruleset): files < 500 lines, 2-space indent, `context.Context` first param across every I/O boundary, `log/slog` with `component` + request-scoped fields, generics for structural contracts / interfaces for behavioral contracts, errors as typed data at package boundaries.

**Domain error type (decided here, used from Phase 0 on):** a small `cogerr` package — `type Error struct { Op string; Kind Kind; Err error }` with `Kind` as an enum (`NotFound`, `Conflict`, `Validation`, `Unavailable`, `Internal`). Every package boundary wraps into this; the MCP layer maps `Kind` to tool-error responses, slog logs `op`/`kind` as fields. Raw `pgx`/HTTP errors never cross a package boundary.

---

## Phase 0 — Foundation

Three independent tracks; parallelizable, no cross-dependencies.

### 0a. Config + XDG + CLI skeleton
**Hypothesis:** Cobra/Viper/adrg-xdg compose without friction; all path resolution can live in one package that nothing else duplicates.
**Deliverables:**
- `internal/config`: XDG-resolved paths (config/data/state per §9.2 table), Viper load with env-var overrides, typed config struct (DSN, embedding provider, personas, bind address)
- `internal/cli`: full command tree from §9 with stub implementations that return "not implemented" — the shape exists before the behavior
- First-start directory creation (`mkdir -p` all four XDG dirs)
**Exit criteria:** `cognosis config get <key>` round-trips a value; `XDG_DATA_HOME=/tmp/x cognosis config get kb_path` proves env-var precedence; all commands appear in `cognosis --help`.

### 0b. Schema + store
**Hypothesis:** the §4.2 schema (notes with full content, derived chunks/links, `embedding_providers` metadata) supports every tool in §6 without a redesign; golang-migrate with embedded SQL auto-applies cleanly at boot.
**Deliverables:**
- `migrations/0001_initial.up.sql`: `notes` (path PK, content, frontmatter columns: id, project, category, status, confidence, maturity, timestamps, mtime, size, blake3_hash), `chunks`, `links`, `embedding_providers`, `schema_migrations` handled by golang-migrate itself
- `internal/store`: connection (pgx), migration runner, CRUD for notes/chunks/links — no embedding tables yet (those are provisioned dynamically in Phase 2)
- `cognosis schema status` wired for real
**Exit criteria:** store tests run against a real local Postgres (Nix dev shell provides it) — upsert note, cascade chunks, rebuild-from-notes proves chunks/links are genuinely derived/droppable. Deterministic: tests create and drop their own schema per run.

### 0c. Vault + frontmatter contract
**Hypothesis:** silo-kb's frontmatter contract ports with only the documented changes (git-staleness → explicit timestamps; folder-as-category → frontmatter `category`).
**Deliverables:**
- `internal/vault`: parse/validate/serialize frontmatter (§4.1 contract + folder layout: `entries/`, `notes/`, `reflections/`, `archive/`), atomic file write (write-temp-then-rename)
- Table-driven validation tests ported from silo-kb's validate package where the rules survived the port
**Exit criteria:** round-trip property holds (parse → serialize → parse is identity); every invalid-frontmatter case from silo-kb's test suite that still applies is covered and rejects with a `Validation`-kind error naming the field.

---

## Phase 1 — Daemon Core + Watcher

Depends on all of Phase 0.

### 1a. Daemon lifecycle
**Hypothesis:** the §7.1 startup order (Postgres → migrations → reconciliation → embedding check → lock → serve) is enforceable as a linear sequence with each step returning a fatal error.
**Deliverables:**
- `internal/daemon`: startup sequence exactly per §7.1, lock file at `$XDG_STATE_HOME/cognosis/daemon.lock` with PID, self-daemonize, graceful shutdown on SIGTERM (context cancellation propagates to watcher/workers); history-repo init on first start (`git init` in the vault if `.git` absent, §4.7)
- `cognosis start` / `stop` / `status` real; `status` reports the three health checks (Postgres, embedding provider, schema currency) — embedding check stubs "skipped" until Phase 2
**Exit criteria:** start with Postgres down → nonzero exit, error names Postgres and the DSN target; second `cognosis start` while running → refused via lock file; `kill -TERM` → clean shutdown, lock released. All three demonstrated as scripted checks, not manual observation.

### 1b. Watcher + boot reconciliation
**Hypothesis:** mtime/size pre-check eliminates ~all boot work on an unchanged vault; BLAKE3 hash path catches editors that don't update mtime; fsnotify + the per-path write lock (§4.5) prevents self-triggering on Cognosis's own writes.
**Deliverables:**
- `internal/watch`: fsnotify loop (create/modify/delete → re-validate → mark dirty; embedding hookup lands in Phase 2), boot reconciliation (fast path + BLAKE3 worker pool) + the periodic sweep (default 60 min per §4.1.1, config-driven), sync-error logging for invalid hand-edits (structured: `path`, `field`, `reason`); confirmed drift gets committed to the history repo as found (§4.7)
**Exit criteria:** boot against a 1k-file synthetic vault with zero changes touches zero hashes (assert via counter, not timing); touch one file's content without changing size+mtime → periodic sweep catches it; a hand-edit with broken frontmatter is logged and *not* indexed, and the previous DB state survives. Non-determinism note: fsnotify event coalescing varies by platform — tests assert eventual convergence (poll with deadline), never event counts.

---

## Phase 2 — Embedding + Write Path

### 2a. Provider abstraction + Ollama
**Hypothesis:** the provider boundary is behavioral (interface: `Embed(ctx, texts) ([][]float32, error)` + `Health(ctx) error` + `Dimension`), while batch plumbing over it is structural (generics) — per §10.1's split.
**Deliverables:**
- `internal/embed`: `Provider` interface, Ollama implementation (task prefixes ported from silo-kb), dynamic table provisioning per §4.3 (probe dimension → `CREATE TABLE embeddings_<slug>` + HNSW index → register in `embedding_providers`), known-dimension shortcut table
- §7.1 embedding health check wired into daemon startup (now fatal for real)
**Exit criteria:** registering a fake provider with an unseen dimension provisions a correctly-shaped table (assert `vector(N)` via catalog query); Ollama integration test embeds and round-trips a vector against local Ollama from the dev shell; daemon start with Ollama stopped → fatal, named error.

### 2b. Chunking + `write_note`
**Hypothesis:** silo-kb's chunking strategy ports intact; the §6 write contract — file write then atomic DB upsert of note+chunks+links+embeddings — is achievable with the file write outside the DB transaction (file first, then one transaction; a crash between the two is exactly what boot reconciliation already repairs, so no distributed-transaction machinery is needed).
**Deliverables:**
- `internal/chunk` ported; write pipeline in `internal/vault`+`store`: validate → write file (under per-path lock, watcher suppressed) → history commit (§4.7) → chunk → embed → single-transaction upsert
**Exit criteria:** crash-injection test (kill between file write and DB commit) followed by boot reconciliation converges to consistent state; concurrent writes to the same path serialize (race test with `-race`); write of an existing note replaces its chunks/embeddings with no orphans (assert row counts); each sanctioned write produces exactly one history commit, and `cognosis vault restore --at` recovers a prior body byte-identically (round-trip test).

---

## Phase 3 — Query + MCP Server

### 3a. RRF retrieval
**Hypothesis:** RRF over three rankers (pgvector cosine, Postgres FTS, link-graph proximity) ports from silo-kb's mechanics with the DB doing per-ranker ranking and Go doing fusion; the fusion merge is a generic function over ranked-list element types.
**Deliverables:**
- `internal/query`: three rankers + generic RRF fusion; `project` scoping; `include_falsified`; fan-out across provisioned providers when >1 exists (§6) — the mechanism lands now because it's the same code path migration fallback (Phase 5) reuses
**Exit criteria:** deterministic fixture corpus (fixed embeddings, no live Ollama in unit tests — a stub provider returns precomputed vectors) with golden-file expected rankings; a document reachable only via graph ranker appears in fused results, proving fusion isn't vector-dominated; `EXPLAIN` output for the vector query confirms HNSW index use (recorded as a test artifact, not eyeballed).

### 3b. MCP server + read tools
**Hypothesis:** Streamable HTTP MCP with a `tools/call`-only surface (§3) covers Claude Code without per-client adapters; tool dispatch is the interface-shaped seam (`Call(ctx, args) (result, error)`).
**Deliverables:**
- `internal/mcpserver`: server, dispatch, tools wired: `write_note`, `query_knowledge`, `list_notes`, `get_note` (auth middleware slot present but pass-through until Phase 4)
- **Security ordering, explicit:** the server binds **loopback-only from its first line of code** — the §11 local-default posture is in force from Phase 3b day one, not retrofitted in 4c. Auth pass-through is acceptable *only because* of this; the bind address does not become configurable to non-loopback values until 4c lands the token check. This ordering is an exit criterion, not a convention.
**Exit criteria:** end-to-end against real Claude Code: write a note via MCP, query it back, confirm retrieval — recorded as a reproducible script (MCP client harness), not a one-off manual session; server provably refuses non-loopback binds (config set to an external address → startup error until 4c). This is the v1 heartbeat demo: **at the end of Phase 3, Cognosis is a working memory system.**

---

## Phase 4 — Lifecycle, Personas, Auth, Hooks

Four tracks, independent of each other; all depend on Phase 3.

### 4a. `compile_lifecycle`
Port silo-kb's compilepass mechanics (reinforce/decay/archive/graduate) + Cognosis's falsify/dispute, under a Postgres advisory lock (§4.5), with `dry_run`. Each run ends in **one history commit** (§4.7), making a whole compile pass a single revertible unit.
**Exit criteria:** state-machine table tests cover every legal transition and reject every illegal one; `dry_run: true` provably writes nothing (assert on DB snapshot diff, and no history commit); concurrent second call gets the explicit already-in-progress error; reverting a run's commit + re-reconciling restores the pre-run vault state end to end.

### 4b. Personas
`internal/persona`: `prompts/personas/` file layout, config-registry sync (§9), `list_personas` (frontmatter-sourced metadata + `responds_to`), `get_persona`, `write_reflection` (validates persona against enabled list, writes to `reflections/` with correct frontmatter). Port the deep-thoughts persona file as the first inhabitant. `persona_filter` fusion bias (§5.4) lands here, on top of 3a's fusion inputs.
**Exit criteria:** `list_personas` returns metadata only (assert response size stays O(personas), not O(file content)); disabled persona rejects `write_reflection` and vanishes from `list_personas` without touching its file; a biased query measurably reorders a fixture corpus vs. unbiased (golden files for both).

### 4c. Auth + audit
`internal/auth`: token create/revoke/list (Argon2id hash storage), per-request synchronous token check (§11 — no cache, deliberately), audit middleware writing `audit_log` with redacted `args_summary`, `token_id` attached to request context and therefore to every slog line.
**Exit criteria:** revoke → very next request 401s (no restart); audit rows contain path/project identifiers but never note content (assert with a content canary string); local-only default posture (loopback bind + auto-generated token) works with zero config.

### 4d. Hooks + context injection
`cognosis context inject --project <p> --budget N` (indexgen analog, daemon-side generation), `.cognosis-project` marker resolution **including the marker gate** (no marker → exit 0 immediately, daemon never contacted, per §16), 2s-timeout/fail-loud behavior per §16; SessionEnd nudge hook script; sample `.claude/settings.json`. The `cognosis vault history`/`restore` CLI (§4.7) also lands here — thin wrappers over the Phase 2b machinery.
**Exit criteria:** in an unmarked repo, hook exits 0 with the daemon stopped (assert zero network calls — the machine-wide-outage regression test); in a marked repo, daemon stopped → hook exits nonzero within ~2s (scripted timing assertion with margin); budget N is respected (token-count the output); marker-file resolution survives a repo relocation.

**Phase 4 exit = v1 feature-complete** for the daily-use path. Systemd unit/launchd plist (§7) also land here — small, and they only invoke the CLI.

---

## Phase 5 — Embedding Migration (§4.3.1)

Deliberately last: it's the most complex machinery, and nothing else depends on it.

**Hypothesis:** the hybrid strategy's two paths are safely idempotent because both reduce to upsert-on-conflict into the new table; the fallback read reuses Phase 3a's multi-provider fan-out rather than introducing a second query path.
**Deliverables:** `internal/migrate`: `migration_state` table (its own schema migration), backfill worker (batched, pausable via context + persisted pause flag, rate-limit aware), lazy migration hooks in query (post-response async) and write (dual-embed during migration), `cognosis embeddings migrate/status/prune`, `get_migration_status` MCP tool, completion flip + rollback flip.
**Exit criteria:** migrate a 5k-chunk fixture corpus between two stub providers with the query path under continuous load — zero queries return empty during migration (the zero-downtime claim, tested, not asserted); pause → resume → completion converges to 100% with `chunks_migrated_backfill + chunks_migrated_lazy == chunks_total`; kill the worker mid-batch → restart resumes with no duplicate embeddings (unique-constraint assertion); rollback mid-migration restores old-provider-only behavior immediately.

---

## Phase 6 — Remaining v1 Extensions

Small, additive, in any order (all already scoped in-v1 by §0.1):
- **`as_of` temporal queries + `list_decaying`** (§12) — frontmatter-timestamp reasoning; golden-file tests over a fixture corpus with synthetic timestamps
- **Cross-project links** (§13) — `project:note-id` link syntax in the links parser + graph ranker
- **Generative summaries / retrieval-augmented compilation** (§12, §14) — thin: these are prompt-shape + tool-arg features over existing machinery
- **Git commit integration** (§15) — opt-in hook, isolated script + one CLI subcommand
- **TLS/reverse-proxy docs** (§11) — documentation + config fallback, minimal code

---

## Test Strategy (answering the ratio question from planning)

- **Unit (majority by count):** frontmatter contract, chunking, RRF fusion (stub provider, golden files), lifecycle state machine, auth token logic, error mapping. Deterministic, no external processes, no mocking of internals — stubs only at the two genuine external boundaries (embedding provider, Postgres — and Postgres only in the narrow cases where a real one is disproportionate).
- **Integration (majority by importance):** store against real Postgres, watcher against real filesystem, Ollama round-trip, daemon startup-sequence failures, migration under load. The Nix dev shell makes "real Postgres + real Ollama, locally, deterministically" cheap — lean on it. Per-run isolated schemas keep tests parallel-safe.
- **End-to-end (few, high-value):** the Phase 3b MCP script; the Phase 4d hook timing script. These are the artifacts that prove the system works, not just its parts.
- **`-race` on everything;** the watcher, write locks, and migration worker are the concurrency hot spots and each has an explicit race test.
- **Flaky = defect:** anything timing-dependent (fsnotify, hook timeout) asserts convergence-within-deadline, never exact timing or event counts.

## Dependency Ledger (each justified once, here)

| Dependency | Justification |
|---|---|
| `pgx` | Postgres driver; stdlib `database/sql` + pq is the alternative but pgx's native pgvector + `LISTEN/NOTIFY` support is load-bearing for §4.3/§11 |
| `pgvector-go` | vector type marshaling for pgx |
| `golang-migrate` | §4.4, decided in design |
| `cobra`, `viper` | §9, decided in design |
| `adrg/xdg` | §9.2, decided in design |
| `fsnotify` | §4.1.1; no stdlib equivalent |
| `zeebo/blake3` | §4.1.1; BLAKE3 justified in design, not in stdlib |
| `mcp-go` (or the official Go SDK, evaluate at Phase 3b) | MCP protocol; hand-rolling Streamable HTTP MCP is real scope for zero benefit |
| `argon2` (`golang.org/x/crypto`) | §11; x/crypto is quasi-stdlib, proven crypto only |
| `mage` | §10, decided in design |
| `git` (system binary, flake-pinned — *not* go-git) | §4.7 vault history; usage is init/add/commit/log/checkout + one filter-repo-style purge on hard-delete — shelling out to the pinned binary is less code and less risk than importing go-git's reimplementation for five subcommands |
| `log/slog` | stdlib — listed to note it is deliberately *not* zap/zerolog |

Anything not on this table needs its own justification before import.

## Milestone Summary

| Milestone | Definition of done |
|---|---|
| **M1** (end Phase 1) | Daemon starts/stops/status correctly, fails fatally per §7.1, reconciles a hand-edited vault |
| **M2** (end Phase 3) | Working memory loop via real Claude Code MCP session — write, retrieve, hybrid-ranked |
| **M3** (end Phase 4) | Feature-complete daily driver: lifecycle, personas, auth, hooks, service files |
| **M4** (end Phase 5) | Zero-downtime provider migration proven under load |
| **M5** (end Phase 6) | v1 complete per §0.1 |

M2 is the point to start dogfooding — run Cognosis as your actual Claude Code memory while building M3–M5, so the remaining phases get built against real usage evidence rather than assumptions.
