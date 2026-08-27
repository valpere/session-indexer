# Migrating `.claude/sessions.db` from schema v2 to v3

Applies when you upgrade `session-indexer` to a version whose
`internal/db.SchemaVersion` is `"3"` (v0.3.0+) and see:

```
error: schema version mismatch (2 != 3): delete .claude/sessions.db and re-mine to rebuild
```

## Read this before you delete anything

`session-indexer`'s general policy — no migration framework, by design —
is "delete the DB and re-mine your JSONLs." That's the right call for a
schema change you can't safely automate. But **check what re-mining can
actually recover before you delete**, especially on a project you've used
for weeks or months:

```bash
# How much history does the DB currently hold?
sqlite3 .claude/sessions.db "SELECT COUNT(*) FROM chunks;"
sqlite3 .claude/sessions.db "SELECT COUNT(DISTINCT session_id) FROM chunks;"

# How much could a re-mine actually recover right now?
ls ~/.claude/projects/<your-project-slug>/*.jsonl 2>/dev/null | wc -l
```

Claude Code prunes old JSONL transcript files after some time. If the chunk
count is much larger than what the remaining JSONLs could reconstruct,
**do not delete** — the v2→v3 change is additive and can be applied in
place instead, with zero data loss.

## What actually changed between v2 and v3

One column. `embeddings` gained `model TEXT NOT NULL` (see PR #56) so
`search`/`embed` can tell which provider/model produced each stored
vector, instead of silently mis-scoring or dropping vectors when you
switch embedding models. Every embedding this schema version has ever
supported was produced by Ollama's `bge-m3:latest` — no other provider
path has shipped — so backfilling that single value for every existing
row is safe.

## In-place migration (recommended over delete-and-re-mine)

```bash
# 1. Back up first. Non-negotiable — this mutates the live DB.
cp .claude/sessions.db .claude/sessions.db.v2-backup-$(date +%Y%m%d-%H%M%S)

# 2. Add the missing column, backfilled for every existing row.
sqlite3 .claude/sessions.db <<'EOF'
ALTER TABLE embeddings ADD COLUMN model TEXT NOT NULL DEFAULT 'ollama:bge-m3:latest';
UPDATE meta SET value='3' WHERE key='schema_version';
EOF

# 3. Bring your session-indexer binary current (if you haven't already).
go install ./cmd/session-indexer   # or: make install

# 4. Verify nothing was lost — compare against the counts you recorded
#    in "Read this before you delete anything" above.
session-indexer stats --db .claude/sessions.db
sqlite3 .claude/sessions.db "SELECT model, COUNT(*) FROM embeddings GROUP BY model;"
```

`Chunks`/`Facts`/`Sessions` counts in `stats` must match what you had
before the `ALTER TABLE` exactly — the migration only adds a column, it
never touches rows. The `GROUP BY model` query should show exactly one
model tag, matching your project's actual embedding history (adjust the
`DEFAULT` value above if you ever used a different `OLLAMA_MODEL` or a
non-default provider for this project — tag rows individually in that
case rather than one blanket default).

Once verified, delete the backup (`.claude/sessions.db.v2-backup-*`) or
keep it — it's gitignored either way (`.claude/sessions.db.*-backup-*`).

## If this isn't your situation

If you're setting up `session-indexer` fresh, or you're fine losing
history and re-mining from scratch, the documented default path (delete +
re-mine) is simpler and works exactly as described in the error message
and `CLAUDE.md`. This guide exists for the case where that default advice
would be actively harmful — an established project where JSONL pruning
has already outpaced what the DB holds.

## For a future schema bump

This guide is specific to 2→3. If `SchemaVersion` bumps again later (e.g.
to `"4"`), check `docs/architecture.md`'s Changelog section and
`internal/db/schema.sql`'s diff for that specific bump — the "is this
additive/backfillable in place" question has to be re-asked for each
transition; the answer here does not generalize.
