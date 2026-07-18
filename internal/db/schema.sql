CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO meta(key, value) VALUES ('schema_version', '2');

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

CREATE TABLE facts (
    id              INTEGER PRIMARY KEY,
    subject         TEXT    NOT NULL,
    predicate       TEXT    NOT NULL,
    object          TEXT    NOT NULL,
    confidence      REAL    NOT NULL,
    source_chunk_id INTEGER REFERENCES chunks(id) ON DELETE SET NULL,
    session_date    TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,
    until           TEXT,
    superseded_by   INTEGER REFERENCES facts(id) ON DELETE SET NULL
);

CREATE VIRTUAL TABLE facts_fts USING fts5(
    subject, predicate, object,
    content='facts', content_rowid='id',
    tokenize="unicode61 remove_diacritics 0"
);

CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, subject, predicate, object)
    VALUES (new.id, new.subject, new.predicate, new.object);
END;
CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
    INSERT INTO facts_fts(facts_fts, rowid, subject, predicate, object)
    VALUES ('delete', old.id, old.subject, old.predicate, old.object);
END;

CREATE TABLE distilled_chunks (
    chunk_id     INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    distilled_at TEXT NOT NULL
);
