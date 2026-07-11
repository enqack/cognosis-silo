{
  description = "silo-template dev environment: Go toolchain + Postgres/pgvector for silo-kb";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        pg = pkgs.postgresql_16.withPackages (p: [ p.pgvector ]);

        # Unix-socket-only Postgres in an in-repo, gitignored data dir.
        # macOS caps sun_path at 104 bytes; fall back to a hashed $TMPDIR dir
        # when the repo path is too deep. That fallback is persisted to
        # .pg-socket-path (gitignored) so the socket — and thus SILOKB_DSN —
        # stays stable across reboots, since macOS randomizes $TMPDIR per boot.
        pgEnv = ''
          export SILOKB_PGDATA="$PWD/.pg-data"
          SOCKDIR="$SILOKB_PGDATA"
          if [ "''${#SOCKDIR}" -gt 90 ]; then
            if [ -f .pg-socket-path ]; then
              SOCKDIR="$(cat .pg-socket-path)"
            else
              SOCKDIR="''${TMPDIR:-/tmp}/silokb-$(echo -n "$PWD" | shasum | cut -c1-12)"
              echo "$SOCKDIR" > .pg-socket-path
            fi
            mkdir -p "$SOCKDIR"
          fi
          export SILOKB_SOCKDIR="$SOCKDIR"
          export SILOKB_DSN="postgres:///silokb?host=$SILOKB_SOCKDIR&port=5433"
        '';

        ollamaUp = ''${pkgs.curl}/bin/curl -sf localhost:11434/api/tags'';

        pg-start = pkgs.writeShellScriptBin "pg-start" ''
          set -euo pipefail
          ${pgEnv}
          if [ ! -d "$SILOKB_PGDATA" ]; then
            ${pg}/bin/initdb -D "$SILOKB_PGDATA" --auth=trust >/dev/null
          fi
          if ! ${pg}/bin/pg_ctl -D "$SILOKB_PGDATA" status >/dev/null 2>&1; then
            ${pg}/bin/pg_ctl -D "$SILOKB_PGDATA" -l "$SILOKB_PGDATA/log" \
              -o "-k $SILOKB_SOCKDIR -p 5433 -c listen_addresses=" start
          fi
          if ! ${pg}/bin/psql -h "$SILOKB_SOCKDIR" -p 5433 -d silokb -c "" >/dev/null 2>&1; then
            ${pg}/bin/createdb -h "$SILOKB_SOCKDIR" -p 5433 silokb
          fi
          PGOPTIONS="-c client_min_messages=warning" ${pg}/bin/psql -h "$SILOKB_SOCKDIR" -p 5433 -d silokb \
            -c "create extension if not exists vector" >/dev/null
          echo "postgres up: $SILOKB_DSN"
        '';

        pg-stop = pkgs.writeShellScriptBin "pg-stop" ''
          set -euo pipefail
          ${pgEnv}
          ${pg}/bin/pg_ctl -D "$SILOKB_PGDATA" stop -m fast
        '';

        # The "droppable derived index" guarantee, literally.
        pg-nuke = pkgs.writeShellScriptBin "pg-nuke" ''
          set -uo pipefail
          ${pgEnv}
          ${pg}/bin/pg_ctl -D "$SILOKB_PGDATA" stop -m fast 2>/dev/null || true
          rm -rf "$SILOKB_PGDATA"
          echo "dropped $SILOKB_PGDATA"
        '';

        # Portable Ollama lifecycle. Prefer whatever is already serving on
        # :11434 — a native macOS app (Metal) or a NixOS services.ollama — and
        # only fall back to the bundled server. Same commands on both platforms.
        # Models live in the default ~/.ollama (shared with a native install).
        ollama-start = pkgs.writeShellScriptBin "ollama-start" ''
          set -uo pipefail
          if ${ollamaUp} >/dev/null 2>&1; then
            echo "ollama already up (native app or system service)"
            exit 0
          fi
          nohup ${pkgs.ollama}/bin/ollama serve >"$PWD/.ollama-serve.log" 2>&1 &
          echo $! > "$PWD/.ollama-serve.pid"
          n=0
          while [ "$n" -lt 50 ]; do
            if ${ollamaUp} >/dev/null 2>&1; then
              echo "ollama up (bundled server, pid $(cat "$PWD/.ollama-serve.pid"))"
              exit 0
            fi
            sleep 0.2
            n=$((n + 1))
          done
          echo "ollama failed to start within 10s — see .ollama-serve.log" >&2
          exit 1
        '';

        # Stops only a server this repo started via ollama-start; a native app
        # or system service is left alone.
        ollama-stop = pkgs.writeShellScriptBin "ollama-stop" ''
          set -uo pipefail
          PIDFILE="$PWD/.ollama-serve.pid"
          if [ -f "$PIDFILE" ]; then
            PID="$(cat "$PIDFILE")"
            kill "$PID" 2>/dev/null && echo "stopped bundled ollama (pid $PID)" \
              || echo "bundled ollama (pid $PID) was not running"
            rm -f "$PIDFILE"
          else
            echo "nothing to stop — ollama-start launched no bundled server (a native app/service is managed on its own)"
          fi
        '';

        # Idempotent full bootstrap: safe to run on every shell entry — each
        # step checks before acting, and no failure aborts the shell.
        silo-init = pkgs.writeShellScriptBin "silo-init" ''
          set -u
          ${pgEnv}
          say()  { echo "silo-init: $*" >&2; }
          warn() { echo "silo-init: warning: $*" >&2; }

          # 1. git
          if [ ! -d .git ]; then
            ${pkgs.git}/bin/git init -q && say "initialized git repository — commit the scaffold when ready"
          fi
          # Point git at the tracked hooks dir so the frontmatter-contract
          # pre-commit gate runs (idempotent; the hook no-ops until silo-kb builds).
          if [ -d .git ] && [ -f .githooks/pre-commit ]; then
            ${pkgs.git}/bin/git config core.hooksPath .githooks
          fi

          # 2. Vault + PROJECTS.md scaffold (fresh silo only — a template clone
          # ships them). Keep this byte-for-byte in sync with
          # tools/silo-kb/internal/scaffold (the Go copy used by `silo-kb reset`).
          if [ ! -d knowledge-base ]; then
            say "scaffolding knowledge-base/ + PROJECTS.md (fresh silo)"
            mkdir -p knowledge-base/daily knowledge-base/deep-thoughts \
              knowledge-base/knowledge/concepts knowledge-base/knowledge/cursed-knowledge \
              knowledge-base/knowledge/lessons-learned \
              knowledge-base/knowledge/archive/faded \
              knowledge-base/projects
            touch knowledge-base/daily/.gitkeep knowledge-base/deep-thoughts/.gitkeep \
              knowledge-base/knowledge/concepts/.gitkeep knowledge-base/knowledge/cursed-knowledge/.gitkeep \
              knowledge-base/knowledge/lessons-learned/.gitkeep \
              knowledge-base/knowledge/archive/faded/.gitkeep \
              knowledge-base/projects/.gitkeep
            cat > knowledge-base/index.md <<'EOF'
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
          EOF
            cat > knowledge-base/knowledge/index.md <<'EOF'
          <!-- GENERATED by silo-kb sync-index — do not hand-edit. Run /kb-sync-index to regenerate. -->

          # Knowledge Index

          _Not yet generated. Run `silo-kb sync-index`._
          EOF
            cat > knowledge-base/knowledge/log.md <<'EOF'
          <!-- Appended by silo-kb compile — audit trail of compilation runs. Do not hand-edit. -->

          # Compilation Log
          EOF
            cat > PROJECTS.md <<'EOF'
          # PROJECTS

          Registry of the independent projects coordinated in this silo. Add one row per project as it is
          onboarded; each project lives in its own repository and earns documentation under
          `knowledge-base/projects/<name>/`.

          | Project | Location | Description | Remote Repository (Git) |
          |---------|----------|-------------|-------------------------|
          EOF
          fi

          # 3. silo-kb binary (Go's build cache makes this a fast no-op)
          if [ -d tools/silo-kb ]; then
            (cd tools/silo-kb && ${pkgs.go}/bin/go build -o silo-kb .) || warn "silo-kb build failed"
          fi

          # 4. Postgres
          ${pg-start}/bin/pg-start >/dev/null || warn "postgres failed to start — see .pg-data/log"

          # 5. Ollama + embedding model. Portable across NixOS/macOS: reuse a
          #    server already up (native app / services.ollama), else start the
          #    bundled one.
          OLLAMA_OK=0
          if ${ollama-start}/bin/ollama-start >&2 && TAGS="$(${ollamaUp} 2>/dev/null)"; then
            if echo "$TAGS" | grep -q 'nomic-embed-text:v1.5'; then
              OLLAMA_OK=1
            else
              say "pulling nomic-embed-text:v1.5 (one-time, ~270 MB)"
              ${pkgs.ollama}/bin/ollama pull nomic-embed-text:v1.5 >&2 && OLLAMA_OK=1 || warn "model pull failed"
            fi
          else
            warn "ollama unavailable — embeddings skipped (start it, then rerun silo-init)"
          fi

          # 6. Derived index
          BIN=tools/silo-kb/silo-kb
          if [ "$OLLAMA_OK" = 1 ] && [ -x "$BIN" ]; then
            "$BIN" reindex >&2 && "$BIN" sync-index >&2 || warn "reindex failed — run: silo-kb reindex"
          fi
        '';

        silo-help = pkgs.writeShellScriptBin "silo-help" ''
          set -u
          ${pgEnv}
          # Everything to stderr so `nix develop --command …` stdout stays clean.
          exec >&2
          ok()  { printf "  \033[32m✓\033[0m %s\n" "$*"; }
          bad() { printf "  \033[31m✗\033[0m %s\n" "$*"; }

          echo
          echo "◆ silo: $(basename "$PWD")"
          echo

          if ${pg}/bin/psql -h "$SILOKB_SOCKDIR" -p 5433 -d silokb -c "" >/dev/null 2>&1; then
            NOTES=$(${pg}/bin/psql -h "$SILOKB_SOCKDIR" -p 5433 -d silokb -tA \
              -c "select count(*) from notes" 2>/dev/null || echo "?")
            CHUNKS=$(${pg}/bin/psql -h "$SILOKB_SOCKDIR" -p 5433 -d silokb -tA \
              -c "select count(*) from chunks" 2>/dev/null || echo "?")
            ok "postgres up ($SILOKB_DSN)"
            if [ "$NOTES" = "?" ] || [ "$NOTES" = "0" ]; then
              bad "index empty — vault has no notes yet (or run: silo-kb reindex)"
            else
              ok "index: $NOTES notes, $CHUNKS chunks"
            fi
          else
            bad "postgres down — run: pg-start (or silo-init)"
          fi

          if TAGS="$(${ollamaUp} 2>/dev/null)"; then
            if echo "$TAGS" | grep -q 'nomic-embed-text:v1.5'; then
              ok "ollama up, nomic-embed-text:v1.5 ready"
            else
              bad "ollama up but nomic-embed-text:v1.5 missing — run: ollama pull nomic-embed-text:v1.5"
            fi
          else
            bad "ollama down — embeddings (reindex/query) unavailable"
          fi

          if [ -x tools/silo-kb/silo-kb ]; then
            ok "silo-kb built (tools/silo-kb/silo-kb)"
          else
            bad "silo-kb not built — run: silo-init"
          fi

          cat <<'EOF'

          environment   pg-start | pg-stop | pg-nuke      postgres lifecycle (index is droppable)
                        ollama-start | ollama-stop        embedding server (reuses a native/system ollama if up)
                        silo-init                         idempotent bootstrap (runs on shell entry)
                        silo-help                         reprint this banner
                        SILOKB_NO_AUTOSTART=1             skip auto-start on nix develop

          silo-kb       reindex [--full]                  delta-sync vault → postgres
                        query "text" [--project P]        hybrid semantic+keyword search
                        compile [--dry-run] [--reinforce …] [--falsify …] [--graduate …]
                                                          knowledge lifecycle: reinforce/decay/falsify/archive/graduate
                        sync-index                        regenerate knowledge/index.md
                        inject-index --budget N           truncated index for session start
                        validate                          check vault frontmatter contract
                        reset --force                     wipe vault to the fresh scaffold (destructive; git is the net)
                        serve-mcp                         stdio MCP server (query_knowledge)

          claude code   /kb-reindex /kb-query /kb-compile /kb-sync-index /kb-reset

          invariants    knowledge-base/ markdown is the single source of truth — pg-nuke is always safe;
                        rebuild with: pg-start && silo-kb reindex --full
                        knowledge-base/knowledge/index.md is GENERATED — never hand-edit it
          EOF
        '';
      in
      {
        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            pg
            pkgs.git
            pkgs.ollama
            pg-start
            pg-stop
            pg-nuke
            ollama-start
            ollama-stop
            silo-init
            silo-help
          ];

          shellHook = ''
            ${pgEnv}
            # Put the in-repo silo-kb binary on PATH so `silo-kb …` works as a
            # bare command (silo-init builds it; it is gitignored and rebuilt
            # from source, not packaged, so source edits are picked up directly).
            export PATH="$PWD/tools/silo-kb:$PATH"
            # Ollama is portable: ollama-start (run by silo-init) reuses a native
            # macOS app or a NixOS services.ollama if one is already serving on
            # :11434, otherwise it starts the bundled server.
            if [ -z "''${SILOKB_NO_AUTOSTART:-}" ]; then
              ${silo-init}/bin/silo-init
            fi
            ${silo-help}/bin/silo-help
          '';
        };
      });
}
