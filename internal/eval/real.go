package eval

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// Snapshot copies the store at src to a new file dst with VACUUM INTO: a
// consistent, defragmented copy taken through a read-only connection, so the
// live store (which a Stop hook may be writing) is never modified and the
// evaluation never races it. dst must not exist.
func Snapshot(src, dst string) error {
	d, err := sql.Open("sqlite", "file:"+src+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer d.Close()
	if _, err := d.Exec("VACUUM INTO '" + strings.ReplaceAll(dst, "'", "''") + "'"); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	return nil
}

// FromStore turns a real session store into an evaluation set: every chunk is a
// "turn" (id "c<chunk id>"), and the questions are distilled facts — query =
// "subject predicate object", evidence = the fact's source chunk. This is
// labelled by distill (an LLM), not by a human, and the query is paraphrased
// from its own source chunk, so it is a proxy, not ground truth. One fact per
// source chunk (the lowest fact id) keeps a heavily-distilled chunk from
// dominating; n facts are then sampled at an even stride, deterministically.
func FromStore(d *sql.DB, n int) (Conversation, error) {
	c := Conversation{ID: "real"}
	rows, err := d.Query(`SELECT id, role, session_date, content FROM chunks ORDER BY id`)
	if err != nil {
		return c, fmt.Errorf("load chunks: %w", err)
	}
	for rows.Next() {
		var id int64
		var t Turn
		if err := rows.Scan(&id, &t.Speaker, &t.Date, &t.Text); err != nil {
			rows.Close()
			return c, err
		}
		t.DiaID = fmt.Sprintf("c%d", id)
		c.Turns = append(c.Turns, t)
	}
	if err := rows.Close(); err != nil {
		return c, err
	}
	frows, err := d.Query(`SELECT subject, predicate, object, source_chunk_id FROM facts
		WHERE id IN (SELECT MIN(id) FROM facts WHERE source_chunk_id IS NOT NULL GROUP BY source_chunk_id)
		ORDER BY source_chunk_id`)
	if err != nil {
		return c, fmt.Errorf("load facts: %w", err)
	}
	defer frows.Close()
	var all []QA
	for frows.Next() {
		var s, p, o string
		var src int64
		if err := frows.Scan(&s, &p, &o, &src); err != nil {
			return c, err
		}
		all = append(all, QA{Question: s + " " + p + " " + o, Evidence: []string{fmt.Sprintf("c%d", src)}})
	}
	if err := frows.Err(); err != nil {
		return c, err
	}
	if n <= 0 || n >= len(all) {
		c.QAs = all
		return c, nil
	}
	for i := 0; i < n; i++ {
		c.QAs = append(c.QAs, all[i*len(all)/n])
	}
	return c, nil
}
