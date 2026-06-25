CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO meta(key, value) VALUES ('schema_version', '1');

CREATE TABLE chunks (
    id            INTEGER PRIMARY KEY,
    session_id    TEXT    NOT NULL,
    session_date  TEXT    NOT NULL,
    role          TEXT    NOT NULL,
    message_index INTEGER NOT NULL,
    chunk_index   INTEGER NOT NULL,
    content       TEXT    NOT NULL,
    created_at    TEXT    NOT NULL
);

CREATE VIRTUAL TABLE chunks_fts USING fts5(
    content,
    content='chunks',
    content_rowid='id',
    tokenize="unicode61 remove_diacritics 0"
);

CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TABLE embeddings (
    chunk_id  INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    vector    BLOB    NOT NULL
);

CREATE UNIQUE INDEX idx_chunks_dedup
    ON chunks(session_id, message_index, chunk_index);
