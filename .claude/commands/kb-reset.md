---
description: Wipe the knowledge base back to the fresh-silo scaffold and rebuild the index
---

**Destructive.** This deletes every note in `knowledge-base/` — all of `daily/`,
`deep-thoughts/`, `knowledge/**`, and `projects/**` — restores the empty template scaffold
(including resetting the repo-root `PROJECTS.md` registry), then rebuilds the derived index.
Git is the only recovery net, so make sure anything worth keeping is committed first.

Confirm the user actually wants to wipe the vault, then build the tool and reset:

```bash
(cd tools/silo-kb && go build -o silo-kb .) && tools/silo-kb/silo-kb reset --force
```

If Postgres isn't running the wipe still succeeds; follow the printed hint
(`pg-start && silo-kb reindex --full`) to rebuild the index. Report what was removed and
the final scaffold state to the user.
