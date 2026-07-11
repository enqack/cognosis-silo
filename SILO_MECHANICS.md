# SILO_MECHANICS.md

The **single source of truth for the silo's mechanics** — the immutable "physics" of this universe:
the vault layout, the knowledge lifecycle and its thresholds, and the frontmatter contract. The three
persona docs each cover their own audience and link here for the rules, so a rule is stated **once**:

- [CLAUDE.md](CLAUDE.md) — the agent-facing operating contract (behaviors, session capture, tooling).
- [HUMAN.md](HUMAN.md) — the operator's digest (stance and day-to-day commands).
- [README.md](README.md) — the newcomer's orientation.

If you are tuning a rule (a decay rate, a threshold, a required field), change it **here**. The persona
docs must not restate these numbers — they point at this file so the human and the agent never drift
onto different baselines of truth. (The executable mechanics live in the Go tool under `tools/silo-kb`;
this document describes what that tool enforces.)

## Layout & tiers

`knowledge-base/` is the knowledge vault and the **single source of truth**. It is an
[Open Knowledge Format (OKF) v0.1](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md)
bundle, also usable directly as an Obsidian vault. Entry point:
[knowledge-base/index.md](knowledge-base/index.md).

```
knowledge-base/
├── daily/          # raw capture: append-only daily session logs         (type: daily-log)
├── deep-thoughts/  # raw capture: one timestamped reflection per file     (type: deep-thought)
├── knowledge/      # working theory: mutable articles, confidence decays  (type: concept, …)
└── projects/       # asserted canon: durable per-project docs, no decay   (type: overview, …)
```

The vault has **two knowledge tiers plus two raw-capture tiers**:

- **Raw capture** — `daily/YYYY-MM-DD.md` (append-only) and `deep-thoughts/`. These are the provenance
  trail. They never graduate directly; `knowledge/` articles cite them via `sources:`.
- **Working theory** — `knowledge/**` is mutable and confidence-decaying: hypotheses that are
  reinforced when re-confirmed and decay when ignored.
- **Asserted canon** — `projects/**` is durable, settled documentation, exempt from the lifecycle. If a
  note lives here, it's asserted, not hypothesized.

The signal is the directory. The markdown tree is authoritative; Postgres (pgvector + full-text, plus a
derived `links` edge table extracted from `sources:` provenance and body wikilinks) is a
**derived, droppable index** — `silo-kb reindex --full` rebuilds it entirely from the working tree. If
the database ever disagrees with the markdown, the database is wrong. Retrieval (`query_knowledge`)
fuses three legs by reciprocal rank fusion: semantic (pgvector) + keyword (full-text) as the primary
signals, plus a weighted graph leg that expands one hop along `links` to boost linked-but-weak notes.

**The database is safe from the agent by construction.** Agents retrieve through the read-only
`query_knowledge` MCP tool and never write to Postgres directly (the interface/implementation boundary
is enforced in `tools/silo-kb/internal/mcpserver/mcpserver.go`). And because Postgres is a derived
index rebuilt from the markdown, nothing done to it — even `pg-nuke` or `silo-kb reset --force` — can
harm the source of truth: git plus the working tree is the recovery net. (This is a property of the
retrieval interface and the derived-index design, not a hard sandbox: an operator, or an agent invoking
the CLI through Bash or a slash command, can still run the administrative commands. The point is that
none of them can *corrupt* canonical knowledge — the markdown is the truth and it is version-controlled.)

**Why OKF:** it's a published, tool-agnostic standard (plain markdown + YAML frontmatter + wikilinks),
so the vault stays portable — it opens as-is in Obsidian and moves to Logseq/Roam without a migration —
and the frontmatter contract below is enforceable independently of any single editor. Nothing here is
load-bearing on OKF-specific machinery; the format is a convention, not a dependency.

**`type` taxonomy** (extend deliberately, don't invent a new value per note):

| `type` | Tier | Meaning |
|---|---|---|
| `daily-log` | `daily/` | append-only capture of a day's work |
| `deep-thought` | `deep-thoughts/` | a single standalone reflection |
| `concept` | `knowledge/` | a reusable pattern/principle/technique (working theory) |
| `overview` | `projects/` | a project-level canon note |

**`knowledge/` subdirectory layout** (organizational only — the generated index groups by `type`, not
by directory; add a subdirectory when it earns its keep, not speculatively):

- `concepts/` — reusable patterns, principles, and techniques (working-theory articles).
- `cursed-knowledge/` — surprising gotchas, i.e. things you wish you didn't have to know. The durable
  home for the `### Cursed Knowledge` entries the session-end extractor
  (`.claude/hooks/session-end-extract.sh`) collects into daily logs; graduate the ones worth keeping
  into an article here.
- `lessons-learned/` — postmortem reflections: what worked, what didn't, and why. Authored
  deliberately (nothing auto-populates it), and worth keeping even when empty.
- `archive/` — retired notes: `archive/faded/` (decayed to zero confidence) and `archive/` (ancient:
  no git commit in >6 months). Falsified notes are **not** archived — they are retained in place (see
  the lifecycle).

## The lifecycle

Run via `/kb-compile` (or `silo-kb compile` by hand). Each run:

- **Reinforces** articles the agent explicitly justifies: +0.1 confidence (capped at 1.0). Mere
  mention doesn't count; nothing is inferred. Reinforcement is also the only thing that promotes
  **maturity**: `seed`→`developing` at confidence ≥0.8; `developing`→`stable` at ≥0.9 with
  `reinforce_count` ≥3. Promotion never happens on decay or by hand-editing.
- **Decays** articles stale >30 days: −0.1 per run — *unless the article is still in use*. Two
  signals suspend decay and reset the clock **without touching confidence** (being referenced keeps a
  note alive; only reinforcement asserts it):
  - **Passive citation-refresh** — a note **cited** (via a `sources:` entry or a body wikilink) by any
    file committed in the last 7 days is refreshed automatically. Citation-as-provenance is treated as an
    implicit signal of continued relevance, so no agent action is required and there is no incentive to
    fake-reinforce just to reset the clock.
  - **`status: paused`** — a note explicitly marked paused (blocked on something external, e.g. waiting
    on a vendor) has its clock frozen until unpaused (`status: active`), so an unfalsified theory isn't
    penalized for a delay it doesn't control. A paused note also can't be ancient-archived or graduated
    while paused.
- **Falsifies** on demand (`--falsify <id>=<reason>`, explicit and agent-justified): a theory judged
  outright false is **retained in place**, not archived — it stays in `knowledge/` stamped
  `status: falsified` with `falsified_reason` and `falsified_at` (the moment we learned it was false),
  optionally with `superseded_by` (`--supersede <id>:<replacement-id>`) pointing at the belief that
  replaced it. The `timestamp`..`falsified_at` span is the window the note was believed, so "what did we
  hold as of date T" stays answerable. A falsified note is frozen and **inert**: it never decays,
  archives, graduates, or serves as a reinforce/graduate target, and it is excluded from default
  retrieval (surfaced only with `--include-falsified`). This wins over reinforce/decay — being wrong is
  distinguished from being forgotten.
- **Disputes** on demand (`--dispute <id>=<reason>`, explicit): a theory the agent contests but has
  **not** disproven is stamped `status: disputed` with `disputed_reason` and `disputed_at`, and **stays
  live and keeps decaying** — recorded dissent, not invalidation. This is the automatic middle ground the
  system uses instead of a human-cleared lock: no frozen fields, no operator unblock step. A later
  `--reinforce` (an agent re-asserting the note) **clears the dispute back to `active`** automatically.
  A single run **rejects contradictory operations on the same note** — reinforcing together with
  falsifying or disputing it — rather than silently applying one and dropping the other, so the invoking
  agent resolves the contradiction with full context. (Because falsify is terminal, reinforce↔falsify
  oscillation across runs cannot occur; the only contradiction window is within a run, which this closes.)
- **Archives** faded articles (confidence ≤ 0 → `knowledge/archive/faded/`) and ancient ones (no git
  commit in >6 months → `knowledge/archive/`). A note reinforced in the same run is shielded from
  ancient-archival.
- **Graduates** on demand (`--graduate <id>:projects/<project>/<note>.md`, explicit and agent-justified
  like reinforcement; only `stable` notes qualify): the article **moves — not copies** — into canon,
  dropping its decay fields and keeping provenance (`sources:`). The destination must be inside a
  project subdirectory — the indexer never sees notes directly under `projects/`. Canon must reflect
  settled belief, so a note that is **`disputed` or `paused` cannot graduate** until that is resolved
  (a reinforce clears a dispute; unpausing sets `status: active`). This is an automatable gate — no
  cross-project bookkeeping — that keeps contested or on-hold theory out of asserted canon.

`knowledge/log.md` is the compilation audit trail, appended by `silo-kb compile` only. `git log` plus
that file explains why any note changed, moved, or vanished.

## The frontmatter contract

Enforced by `silo-kb validate` (the PreToolUse hook inside Claude Code and the pre-commit gate).

- **Reserved filenames:** `index.md` and `log.md` are exempt from the `type` requirement. Only the root
  `knowledge-base/index.md` may carry frontmatter, and only `okf_version: "0.1"` — every other
  `index.md`/`log.md` must have no frontmatter at all.
- **Every other `.md` note** needs YAML frontmatter with a required `type` field and a stable `id`
  (UUID, assigned once — UUIDv7 for new notes — never reassigned; moves keep the `id`, and the index
  keys on it). Recommended fields, in order: `title`, `description`, `resource` (a repo-relative path),
  `tags`, `timestamp`.
- **`knowledge/**` (working theory)** additionally requires: `confidence` (0–1), `maturity`
  (`seed`|`developing`|`stable`), `last_reinforced`, `reinforce_count`, and a non-empty `sources` list.
  Every `sources` entry must **resolve** to an existing `daily/` or `deep-thought` capture — an
  unresolved provenance link is a hard `silo-kb validate` failure (checked whole-vault, so it is
  surfaced by the CLI/pre-commit gate rather than the per-file PreToolUse hook). Optional `status`:
  `active` (default), `disputed`, `paused`, or `falsified`. A `disputed` note may carry
  `disputed_reason` and `disputed_at` (stamped by `compile --dispute`). A `falsified` note additionally
  carries `falsified_reason` and `falsified_at`, and may carry `superseded_by` (a wikilink).
- **`deep-thoughts/**` (raw capture)** must carry a non-empty plain-text `description`: a dry, literal
  one-sentence summary of the session event. This — not the comedic Jack-Handey body — is what the
  indexer embeds and full-text-indexes, so the humor stays for humans while the semantic index reflects
  the real technical event. (Changing this requires a full reindex to re-chunk existing deep-thoughts.)
- **`projects/**` (asserted canon)** must **NOT** carry any decay fields.
- **Timestamps** are `YYYY-MM-DD HH:MM:SS` (local time) everywhere a full timestamp appears — except
  daily-log capture-batch headings, which are time-only `## HH:MM:SS` (the file is already dated by its
  frontmatter and `# <date>` h1).
- **Links:** internal vault links use Obsidian wikilinks (`[[note]]`); citations to actual repo files
  use plain relative markdown links.
- **Never hand-edit** `knowledge/index.md` (GENERATED by `silo-kb sync-index`) or `knowledge/log.md`
  (appended by `silo-kb compile` only). The write-time hook blocks agents from editing these and from
  writing notes with broken frontmatter; a tracked git pre-commit hook (`.githooks/pre-commit`, wired
  by `silo-init` via `core.hooksPath`) runs the same validator so the contract holds outside Claude
  Code too.
