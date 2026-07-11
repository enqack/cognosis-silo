---
description: Rebuild/delta-sync the derived Postgres index from the vault
---

Build the tool and reindex the knowledge base, then report the counts:

```bash
(cd tools/silo-kb && go build -o silo-kb .) && tools/silo-kb/silo-kb reindex $ARGUMENTS
```

If Postgres isn't running, run `pg-start` first (inside `nix develop`). Pass `--full`
to truncate and rebuild everything. Report the note/chunk counts to the user.
