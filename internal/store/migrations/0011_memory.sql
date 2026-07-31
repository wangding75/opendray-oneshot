-- 0011_memory — opendray's optional cross-session memory layer.
--
-- The pgvector extension itself must be installed by an operator
-- with superuser privileges before this migration runs (one-time,
-- lives outside opendray's normal user). This file is idempotent
-- if the extension is already present and the table doesn't exist.
--
-- Storage shape:
--   id              opaque opendray id ("mem_<base64>")
--   scope           "session" | "project" | "global"
--   scope_key       session_id | project cwd | operator name
--   text            human-readable fact body
--   embedding       vector(D) — D set per-row so we can support
--                   different embedders coexisting (BM25 = 384,
--                   bge-m3 = 1024). Indices are per-dim.
--   embedder        identifier of the embedder that produced the
--                   vector ("bm25", "http:bge-m3", etc.) — readers
--                   filter by this to keep cosine comparisons honest.
--   metadata        free-form JSON for caller-supplied tags
--   created_at      insert time
--   updated_at      bumped on dedupe-merge
--
-- We don't index `embedding` here. pgvector HNSW requires a fixed
-- vector dimension; since we let multiple embedders co-exist, we
-- create the index lazily from Go after we see the first vector
-- of each (embedder, dim) combination. See pgvector_store.go.

CREATE TABLE IF NOT EXISTS memories (
    id          TEXT PRIMARY KEY,
    scope       TEXT NOT NULL CHECK (scope IN ('session', 'project', 'global')),
    scope_key   TEXT NOT NULL,
    text        TEXT NOT NULL,
    -- The vector column is created without a fixed dim so
    -- different embedders can coexist; pgvector's vector type
    -- supports unspecified dimensionality at the column level
    -- (per-row dim is stored).
    embedding   vector,
    embedder    TEXT NOT NULL,
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS memories_scope_idx
    ON memories (scope, scope_key);

CREATE INDEX IF NOT EXISTS memories_embedder_idx
    ON memories (embedder);

-- Track which (embedder, dim) HNSW indices we've created lazily so
-- the runtime doesn't keep retrying CREATE INDEX. opendray writes
-- one row here on first vector observed.
CREATE TABLE IF NOT EXISTS memory_index_state (
    embedder    TEXT PRIMARY KEY,
    dim         INTEGER NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
