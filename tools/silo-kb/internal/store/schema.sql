-- Derived index over knowledge-base/. Droppable; rebuilt by `silo-kb reindex --full`.
create extension if not exists vector;

create table if not exists notes (
  id           uuid primary key,      -- from frontmatter, never generated here
  project      text not null default '',
  path         text not null,         -- mutable, updated on move/graduation
  type         text not null,
  frontmatter  jsonb not null,
  content_hash text not null,         -- sha256 of post-frontmatter body
  updated_at   timestamptz not null
);

create table if not exists chunks (
  id           uuid primary key default gen_random_uuid(),
  note_id      uuid not null references notes(id) on delete cascade,
  ordinal      int not null,          -- positional chunk identity within the note
  heading_path text,                  -- null for single-chunk types (deep-thoughts)
  content      text not null,
  content_hash text not null,         -- chunk-level, enables delta reindex
  embedding    vector(768),           -- nomic-embed-text
  fts          tsvector generated always as (to_tsvector('english', content)) stored,
  unique (note_id, ordinal)
);

-- Resolved outbound links between notes: body wikilinks (kind='wikilink') and
-- `sources` provenance (kind='source'). Derived from the markdown, rebuilt on
-- every reindex. Powers the graph leg of retrieval and provenance traversal.
-- Kept as a relational edge list (not a graph extension) so it stays PG16-native
-- and, in a future PG19, is a drop-in property-graph view.
create table if not exists links (
  src_note_id uuid not null references notes(id) on delete cascade,
  dst_note_id uuid not null references notes(id) on delete cascade,
  kind        text not null,          -- 'wikilink' | 'source'
  primary key (src_note_id, dst_note_id, kind)
);

create index if not exists notes_project_idx on notes (project);
create index if not exists notes_path_idx on notes (path);
create index if not exists chunks_embedding_idx on chunks using hnsw (embedding vector_cosine_ops);
create index if not exists chunks_fts_idx on chunks using gin (fts);
create index if not exists links_dst_idx on links (dst_note_id);
