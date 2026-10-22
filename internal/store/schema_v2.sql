-- v2: bookkeeping for incremental resolution (see docs/DECISIONS.md D11).

-- Files whose outgoing resolved edges must be recomputed. changed holds
-- store.Changed* bits for files whose own content changed (0 for files
-- that are dirty only as dependents).
CREATE TABLE resolve_dirty(
  file_id INTEGER PRIMARY KEY,
  changed INTEGER NOT NULL DEFAULT 0
);

-- Lets dirty marking find refs importing a given member name.
CREATE INDEX refs_member ON refs(member) WHERE member != '';

-- Per-file resolution counts by ref type, for stats.
CREATE TABLE resolve_stats(
  file_id  INTEGER NOT NULL,
  type     INTEGER NOT NULL,
  total    INTEGER NOT NULL,
  resolved INTEGER NOT NULL,
  PRIMARY KEY(file_id, type)
) WITHOUT ROWID;
