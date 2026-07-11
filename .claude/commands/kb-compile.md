---
description: Run a knowledge compilation pass (reinforce/decay/falsify/archive/graduate)
---

Run a knowledge compilation pass. Reinforcement, falsification, and graduation are all
explicit and must be justified — nothing is inferred from mere mentions.

1. Read `knowledge-base/knowledge/log.md` to find the previous run, then read the
   daily logs and deep-thoughts written since.
2. Decide which `knowledge/**` articles were genuinely re-confirmed by that material
   (an article being *used or validated* counts; being *mentioned* does not). Decide
   whether any theory has been **determined false** by that material — falsification
   archives it with a recorded reason rather than letting it passively decay. Decide
   whether any stable, high-confidence article should graduate to `projects/<project>/`,
   and where.
3. Show a dry run and your justification for each reinforcement/falsification/graduation:
   ```bash
   tools/silo-kb/silo-kb compile --dry-run --reinforce <id-or-path>,... \
     [--falsify <id-or-path>=<reason>] [--graduate <id>:projects/<proj>/<name>.md]
   ```
4. After the user confirms, run it for real (drop `--dry-run`), then:
   ```bash
   tools/silo-kb/silo-kb reindex && tools/silo-kb/silo-kb sync-index
   ```
5. Report what changed: reinforced, decayed, falsified, archived, graduated — with the log.md entry.

Note: a live article the agent contests but hasn't disproven can be marked `status: disputed`
in its frontmatter (kept live, dissent recorded); `--falsify` is for theories judged outright false.
