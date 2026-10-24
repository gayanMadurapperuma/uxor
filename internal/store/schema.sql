-- uxor schema v1. Never stores source code: only paths, line ranges,
-- names, signatures and hashes. Enum columns use parse.Kind / parse.EdgeType.

CREATE TABLE files(
  id    INTEGER PRIMARY KEY,
  path  TEXT NOT NULL UNIQUE,          -- root-relative, forward slashes
  lang  TEXT NOT NULL,
  hash  BLOB NOT NULL,                 -- sha256[:16] of the content
  size  INTEGER NOT NULL,
  mtime INTEGER NOT NULL,              -- unix nanos; unchanged size+mtime skips hashing
  loc   INTEGER NOT NULL
);

CREATE TABLE symbols(
  id             INTEGER PRIMARY KEY,
  file_id        INTEGER NOT NULL,
  kind           INTEGER NOT NULL,
  name           TEXT NOT NULL,
  qualified_name TEXT NOT NULL,
  signature      TEXT NOT NULL DEFAULT '',
  start_line     INTEGER NOT NULL,
  end_line       INTEGER NOT NULL,
  exported       INTEGER NOT NULL DEFAULT 0,
  pagerank       REAL NOT NULL DEFAULT 0
);
CREATE INDEX symbols_file ON symbols(file_id);
CREATE INDEX symbols_name ON symbols(name);
CREATE INDEX symbols_qname ON symbols(qualified_name);

-- Every file has one kind=module symbol, so edges are uniformly
-- symbol→symbol (IMPORTS module→module, DEFINES parent→child).
CREATE TABLE edges(
  src_id INTEGER NOT NULL,
  dst_id INTEGER NOT NULL,
  type   INTEGER NOT NULL,
  weight REAL NOT NULL DEFAULT 1,
  PRIMARY KEY(src_id, type, dst_id)
) WITHOUT ROWID;
CREATE INDEX edges_dst ON edges(dst_id, type, src_id);

-- Unresolved references by name: the durable source of truth for
-- cross-file edges, which the resolver derives from these.
CREATE TABLE refs(
  src_id    INTEGER NOT NULL,
  file_id   INTEGER NOT NULL,
  type      INTEGER NOT NULL,
  name      TEXT NOT NULL,
  qualifier TEXT NOT NULL DEFAULT '',
  member    TEXT NOT NULL DEFAULT '',
  line      INTEGER NOT NULL
);
CREATE INDEX refs_file ON refs(file_id);
CREATE INDEX refs_name ON refs(name);

-- Contentless FTS over identifier-split text; rowid = symbols.id.
CREATE VIRTUAL TABLE symbols_fts USING fts5(
  name, qualified_name, signature, path,
  content='', contentless_delete=1,
  tokenize='unicode61 remove_diacritics 2'
);

CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
