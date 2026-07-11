---
okf_version: "0.1"
---

# Knowledge Base

Entry point for this silo's OKF bundle / Obsidian vault.

- `daily/` — raw session capture, append-only daily logs
- `deep-thoughts/` — raw capture, one timestamped reflection per file
- `knowledge/` — working theory: mutable, confidence-decaying articles ([[knowledge/index|generated index]])
- `projects/` — asserted canon: durable per-project documentation, no decay

The markdown tree is the single source of truth. The Postgres index is derived and
droppable; rebuild it with `silo-kb reindex --full`.
