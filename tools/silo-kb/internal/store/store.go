// Package store owns the Postgres side of the derived index: connection,
// idempotent schema migration, and the delta reindex algorithm.
package store

import (
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"silo.local/silo-kb/internal/chunk"
	"silo.local/silo-kb/internal/links"
	"silo.local/silo-kb/internal/vault"
)

//go:embed schema.sql
var schemaSQL string

// DSN resolves the connection string: $SILOKB_DSN wins; otherwise self-locate
// by walking up from cwd to a .pg-data dir (matches the flake's pg-start
// layout). Self-location keeps .mcp.json and hooks free of env-expansion
// fragility.
func DSN() (string, error) {
	if dsn := os.Getenv("SILOKB_DSN"); dsn != "" {
		return dsn, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		pgdata := filepath.Join(dir, ".pg-data")
		if st, err := os.Stat(pgdata); err == nil && st.IsDir() {
			sock, err := sockDir(dir, pgdata)
			if err != nil {
				return "", err
			}
			return "postgres:///silokb?host=" + url.QueryEscape(sock) + "&port=5433", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("SILOKB_DSN unset and no .pg-data found above cwd — run pg-start first")
		}
		dir = parent
	}
}

// sockDir mirrors the flake's macOS sun_path-length fallback exactly: when the
// .pg-data path is too long for a Unix socket, the flake persists the actual
// socket dir to <repo>/.pg-socket-path — read that first, and only if it's
// missing recompute the same hashed $TMPDIR path pg-start would pick
// (sha1 of the repo dir, first 12 hex chars).
func sockDir(repoDir, pgdata string) (string, error) {
	if len(pgdata) <= 90 {
		return pgdata, nil
	}
	if b, err := os.ReadFile(filepath.Join(repoDir, ".pg-socket-path")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	sum := sha1.Sum([]byte(repoDir))
	return filepath.Join(tmp, "silokb-"+hex.EncodeToString(sum[:])[:12]), nil
}

func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	dsn, err := DSN()
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Migrate applies the embedded schema idempotently.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}

// Embedder is satisfied by embed.Client; injected so reindex is testable.
type Embedder interface {
	Warmup(ctx context.Context) error
	Documents(ctx context.Context, texts []string) ([][]float32, error)
}

type ReindexStats struct {
	Notes          int
	SkippedNotes   int
	MovedNotes     int
	ChunksKept     int
	ChunksEmbedded int
	NotesPruned    int64
	Links          int
}

// Reindex delta-syncs the vault into Postgres in a single transaction: an
// embedding failure aborts the whole run — a partially embedded index is
// worse than a stale one, and hashes make reruns cheap.
func Reindex(ctx context.Context, pool *pgxpool.Pool, emb Embedder, notes []*vault.Note, full bool) (*ReindexStats, error) {
	if err := Migrate(ctx, pool); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if full {
		if _, err := tx.Exec(ctx, "truncate chunks, links, notes"); err != nil {
			return nil, err
		}
	}

	stats := &ReindexStats{}
	warmed := false
	seen := make([]uuid.UUID, 0, len(notes))

	for _, n := range notes {
		id, err := uuid.Parse(n.ID())
		if err != nil {
			return nil, fmt.Errorf("%s: bad id: %w", n.Path, err)
		}
		seen = append(seen, id)
		stats.Notes++

		var oldHash, oldPath string
		err = tx.QueryRow(ctx, "select content_hash, path from notes where id=$1", id).Scan(&oldHash, &oldPath)
		exists := err == nil
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}

		fmJSON, err := json.Marshal(n.Frontmatter)
		if err != nil {
			return nil, fmt.Errorf("%s: frontmatter to json: %w", n.Path, err)
		}

		if exists && oldHash == n.ContentHash {
			// Body unchanged: chunks stay. Refresh metadata (covers moves —
			// graduation/archival — and frontmatter-only edits) without
			// touching embeddings.
			if oldPath != n.Path {
				stats.MovedNotes++
			} else {
				stats.SkippedNotes++
			}
			if _, err := tx.Exec(ctx,
				`update notes set path=$2, project=$3, type=$4, frontmatter=$5, updated_at=now() where id=$1`,
				id, n.Path, n.Project, n.Type(), fmJSON); err != nil {
				return nil, err
			}
			continue
		}

		if _, err := tx.Exec(ctx, `
			insert into notes (id, project, path, type, frontmatter, content_hash, updated_at)
			values ($1,$2,$3,$4,$5,$6,now())
			on conflict (id) do update set project=excluded.project, path=excluded.path,
				type=excluded.type, frontmatter=excluded.frontmatter,
				content_hash=excluded.content_hash, updated_at=now()`,
			id, n.Project, n.Path, n.Type(), fmJSON, n.ContentHash); err != nil {
			return nil, err
		}

		newChunks := chunk.Split(n)

		// Existing chunk hashes by ordinal — the delta diff.
		oldChunks := map[int]string{}
		rows, err := tx.Query(ctx, "select ordinal, content_hash from chunks where note_id=$1", id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ord int
			var h string
			if err := rows.Scan(&ord, &h); err != nil {
				rows.Close()
				return nil, err
			}
			oldChunks[ord] = h
		}
		rows.Close()

		var toEmbed []chunk.Chunk
		for _, c := range newChunks {
			if oldChunks[c.Ordinal] == c.Hash {
				stats.ChunksKept++
				continue
			}
			toEmbed = append(toEmbed, c)
		}

		// Drop ordinals that changed or fell off the end.
		if _, err := tx.Exec(ctx,
			"delete from chunks where note_id=$1 and ordinal >= $2", id, len(newChunks)); err != nil {
			return nil, err
		}
		if len(toEmbed) > 0 {
			if !warmed {
				if err := emb.Warmup(ctx); err != nil {
					return nil, fmt.Errorf("embedder warmup: %w", err)
				}
				warmed = true
			}
			texts := make([]string, len(toEmbed))
			for i, c := range toEmbed {
				texts[i] = c.Content
			}
			vecs, err := emb.Documents(ctx, texts)
			if err != nil {
				return nil, fmt.Errorf("%s: embed: %w", n.Path, err)
			}
			for i, c := range toEmbed {
				if _, err := tx.Exec(ctx, `
					insert into chunks (note_id, ordinal, heading_path, content, content_hash, embedding)
					values ($1,$2,nullif($3,''),$4,$5,$6)
					on conflict (note_id, ordinal) do update set heading_path=excluded.heading_path,
						content=excluded.content, content_hash=excluded.content_hash,
						embedding=excluded.embedding`,
					id, c.Ordinal, c.HeadingPath, c.Content, c.Hash, pgvector.NewVector(vecs[i])); err != nil {
					return nil, err
				}
				stats.ChunksEmbedded++
			}
		}
	}

	// Prune notes deleted from the vault (cascade removes their chunks + links).
	tag, err := tx.Exec(ctx, "delete from notes where not (id = any($1))", seen)
	if err != nil {
		return nil, err
	}
	stats.NotesPruned = tag.RowsAffected()

	// Rebuild the link graph from scratch: it is small and fully derived from
	// the (now current) note set, so a wholesale rebuild is simpler and more
	// robust than delta-tracking edges, and every FK target is guaranteed
	// present. Dangling links (target basename with no note) are dropped.
	linkCount, err := rebuildLinks(ctx, tx, notes)
	if err != nil {
		return nil, fmt.Errorf("rebuild links: %w", err)
	}
	stats.Links = linkCount

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return stats, nil
}

// rebuildLinks replaces the whole links table with the edges resolvable from
// notes' `sources` frontmatter and body wikilinks. Basenames resolve to note
// ids via a first-wins map (ids are the unique key; basenames are only
// effectively unique). Self-links and dangling targets are skipped.
func rebuildLinks(ctx context.Context, tx pgx.Tx, notes []*vault.Note) (int, error) {
	byName := make(map[string]uuid.UUID, len(notes))
	for _, n := range notes {
		id, err := uuid.Parse(n.ID())
		if err != nil {
			return 0, err
		}
		bn := strings.TrimSuffix(filepath.Base(n.Path), ".md")
		if _, dup := byName[bn]; !dup {
			byName[bn] = id
		}
	}

	if _, err := tx.Exec(ctx, "delete from links"); err != nil {
		return 0, err
	}

	count := 0
	for _, n := range notes {
		srcID, err := uuid.Parse(n.ID())
		if err != nil {
			return 0, err
		}
		for _, ref := range links.Targets(n) {
			dstID, ok := byName[ref.Name]
			if !ok || dstID == srcID {
				continue
			}
			tag, err := tx.Exec(ctx, `
				insert into links (src_note_id, dst_note_id, kind)
				values ($1,$2,$3) on conflict do nothing`,
				srcID, dstID, string(ref.Kind))
			if err != nil {
				return 0, err
			}
			count += int(tag.RowsAffected())
		}
	}
	return count, nil
}
