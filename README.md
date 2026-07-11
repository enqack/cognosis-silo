# silo-template

A **silo** is a self-contained coordination center for independent agentic projects — not a single
codebase, but a workspace where a human operator and AI agents (Claude Code) share long-lived context:
projects, working theories, settled decisions, and the tooling to search all of it. This repository is
the template; clone or copy it to start a new silo, run `nix develop`, and everything bootstraps itself.

This README is the newcomer's orientation. [HUMAN.md](HUMAN.md) is the operator's day-to-day digest,
[CLAUDE.md](CLAUDE.md) is the agent-facing contract, and [SILO_MECHANICS.md](SILO_MECHANICS.md) is the
single source of truth for the mechanics all of them rely on (layout, lifecycle, frontmatter contract).

## What this is

One silo coordinates many independent projects. Instead of scattering context across repos and chat
logs, a silo keeps it in one searchable vault that both you and the agents read and write. The projects
a silo coordinates are registered in [PROJECTS.md](PROJECTS.md).

## Layout & tiers

```
silo-template/
├── knowledge-base/        # the knowledge vault — the single source of truth (markdown/OKF/Obsidian)
│   ├── daily/             # raw capture: append-only daily session logs
│   ├── deep-thoughts/     # raw capture: one timestamped reflection per file
│   ├── knowledge/         # working theory: mutable articles with confidence that decays
│   └── projects/          # asserted canon: durable per-project documentation, never decays
├── tools/silo-kb/         # Go tooling: derived Postgres index, lifecycle engine, MCP server
├── prompts/               # creative prompt files kept out of the operating contract (e.g. deep-thoughts persona)
├── workspace/             # scratch area for project checkouts and working files
├── flake.nix              # Nix dev environment: auto-bootstraps and auto-starts the silo
├── PROJECTS.md            # registry of the projects this silo coordinates
├── SILO_MECHANICS.md      # single source of truth for the mechanics (layout, lifecycle, frontmatter)
├── CLAUDE.md              # the operating contract for AI agents
└── HUMAN.md               # the operator's digest for the human
```

The design enforces an honest split across three stances:

- **Working theories** (`knowledge/`) carry a confidence score, reinforced when re-confirmed and
  decaying when ignored.
- **Asserted canon** (`projects/`) is settled documentation — it never decays and is never touched by
  the lifecycle. If it's there, it's asserted, not hypothesized.
- **Raw capture** (`daily/`, `deep-thoughts/`) is the provenance trail both tiers cite.

The markdown tree is the **single source of truth**. Postgres (pgvector + full-text, plus a link graph
derived from wikilinks and `sources:` provenance) is a derived search index — droppable at any time
(`pg-nuke`) and rebuilt from the working tree in one command. If
the database ever disagrees with the markdown, the database is wrong. This is also why the agents can't
harm your knowledge through the database: they retrieve through a **read-only** MCP tool
(`query_knowledge`) and never write Postgres directly, and the index is disposable — everything durable
lives in version-controlled markdown.

## The lifecycle

Knowledge here has a **lifecycle**. Theories are reinforced when re-confirmed and decay when ignored;
ones that fade to zero are archived, and ones that prove stable **graduate** into project canon. Canon
never decays. The full mechanics (thresholds, maturity promotion, falsification) live in
[SILO_MECHANICS.md](SILO_MECHANICS.md).

## The frontmatter contract

Every note carries YAML frontmatter with a stable `id` and a `type`; working-theory notes add
confidence/maturity fields that canon notes must not have. A validator (a write-time hook and a git
pre-commit gate) enforces it. See [SILO_MECHANICS.md](SILO_MECHANICS.md) for the exhaustive contract
and [HUMAN.md](HUMAN.md) for the operator summary.

## Tooling & commands

```sh
nix develop        # bootstraps everything: git, vault, build, Postgres, model, index
```

The shell banner shows live status (✓/✗) and the full command reference; reprint it any time with
`silo-help`. The only requirement is Nix (flakes enabled) — the dev shell bundles everything else,
including [Ollama](https://ollama.com) for embeddings. If you already run a native Ollama (the macOS app
for Metal, or a NixOS `services.ollama`), the bootstrap reuses it instead of the bundled server.

**Retrieval:** every note is chunked, embedded locally (Ollama, `nomic-embed-text:v1.5`), and indexed
for hybrid search — three legs fused with reciprocal rank fusion: semantic similarity and keyword rank
as the equal-weighted primary signals, plus a lower-weighted graph leg that expands one hop along
wikilink/provenance edges to surface linked-but-weak notes. Falsified (retired-but-retained) notes are
excluded by default. Agents reach it through an MCP tool (`query_knowledge`); humans through
`silo-kb query "…"`. Day-to-day commands are in [HUMAN.md](HUMAN.md).

## Automatic session behavior

In a Claude Code session the agents work the vault on their own: a knowledge index is injected at
session start, frontmatter is validated on every write, agents capture durable material to the daily
log and write the occasional deep thought, and a session-end pass sweeps up anything missed. See
[HUMAN.md](HUMAN.md) for the full list.

## Adding a project — and starting a new silo

To add a project to an existing silo: register it in the table in [PROJECTS.md](PROJECTS.md), then add
`knowledge-base/projects/<name>/` notes as they earn documentation.

To start a brand-new silo from this template:

1. Copy or clone this repository under a new name.
2. `nix develop` — `silo-init` scaffolds anything missing and starts the stack.
3. Register your projects in [PROJECTS.md](PROJECTS.md) and add `knowledge-base/projects/<name>/` notes
   as they earn documentation.
