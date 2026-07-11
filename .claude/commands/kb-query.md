---
description: Hybrid semantic+keyword search over the knowledge base
---

Search the knowledge base and show the results:

```bash
tools/silo-kb/silo-kb query "$ARGUMENTS"
```

(Build with `cd tools/silo-kb && go build -o silo-kb .` if the binary is missing.
Use `--project <name>` to restrict to one project, `--top-k N` for more results.)
