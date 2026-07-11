// Package query implements hybrid retrieval: cosine similarity over pgvector
// fused with ts_rank over native tsvector via reciprocal rank fusion, plus a
// weighted graph leg that expands one hop along the links table — all in one
// SQL round trip. The two primary legs always run; the graph leg only boosts.
package query

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

const (
	rrfK          = 60 // standard RRF constant
	candidatePool = 50 // per-leg candidates before fusion
	DefaultTopK   = 8
	graphWeight   = 0.5 // graph leg is a booster, weighted below the two primary legs
)

type Result struct {
	Path        string
	Type        string
	HeadingPath string
	Content     string
	Score       float64
}

// $7 = includeFalsified: when false, notes carrying `status: falsified` are
// dropped from both legs so retrieval reflects "what's true now". `is distinct
// from` keeps notes with no status (null ⇒ active).
const sql = `
with vec as (
  select c.id, row_number() over (order by c.embedding <=> $1) as r
  from chunks c join notes n on n.id = c.note_id
  where ($3 = '' or n.project = $3) and c.embedding is not null
    and ($7 or (n.frontmatter->>'status') is distinct from 'falsified')
  order by c.embedding <=> $1
  limit $5
), kw as (
  select c.id, row_number() over (order by ts_rank_cd(c.fts, q) desc) as r
  from chunks c
  join notes n on n.id = c.note_id,
  websearch_to_tsquery('english', $2) q
  where c.fts @@ q and ($3 = '' or n.project = $3)
    and ($7 or (n.frontmatter->>'status') is distinct from 'falsified')
  order by r
  limit $5
), seeds as (
  -- the notes behind the semantic+keyword candidates
  select distinct n.id as note_id
  from (select id from vec union select id from kw) s
  join chunks c on c.id = s.id
  join notes n on n.id = c.note_id
), graph as (
  -- one hop out along links from those seeds: chunks of linked notes, ranked by
  -- how many seeds reach them. $8 weights this below the primary legs.
  select c.id, row_number() over (order by count(*) desc) as r
  from links l
  join chunks c on c.note_id = l.dst_note_id
  join notes n on n.id = c.note_id
  where l.src_note_id in (select note_id from seeds)
    and ($7 or (n.frontmatter->>'status') is distinct from 'falsified')
  group by c.id
  limit $5
)
-- Reciprocal Rank Fusion: semantic + keyword vote with equal weight; the graph
-- leg is added as a $8-scaled booster so linked-but-weak chunks surface. To bias
-- the primary legs, scale one, e.g. 0.8*1.0/($6+vec.r) + 0.2*1.0/($6+kw.r).
select n.path, n.type, coalesce(c.heading_path, ''), c.content,
       coalesce(1.0/($6 + vec.r), 0) + coalesce(1.0/($6 + kw.r), 0)
       + $8 * coalesce(1.0/($6 + graph.r), 0) as score
from chunks c
join notes n on n.id = c.note_id
left join vec on vec.id = c.id
left join kw on kw.id = c.id
left join graph on graph.id = c.id
where vec.id is not null or kw.id is not null or graph.id is not null
order by score desc
limit $4`

// Run fuses both retrieval legs. queryVec must be embedded with the
// search_query task prefix (embed.Client.Query does this). includeFalsified
// keeps retained-but-invalidated notes in the results (for as-of/history
// queries); the default excludes them.
func Run(ctx context.Context, pool *pgxpool.Pool, queryVec []float32, queryText, project string, topK int, includeFalsified bool) ([]Result, error) {
	if topK <= 0 {
		topK = DefaultTopK
	}
	rows, err := pool.Query(ctx, sql,
		pgvector.NewVector(queryVec), queryText, project, topK, candidatePool, rrfK, includeFalsified, graphWeight)
	if err != nil {
		return nil, fmt.Errorf("rrf query: %w", err)
	}
	defer rows.Close()

	var out []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Path, &r.Type, &r.HeadingPath, &r.Content, &r.Score); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
