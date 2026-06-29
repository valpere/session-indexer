package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
	"github.com/valpere/session-indexer/internal/embed"
	"github.com/valpere/session-indexer/internal/mine"
	"github.com/valpere/session-indexer/internal/search"
)

// version is the current release; overridden at build time via
// -ldflags "-X main.version=x.y.z" (see Makefile).
var version = "0.1.0"

func main() {
	var dbPath string

	root := &cobra.Command{
		Use:           "session-indexer",
		Short:         "Index and semantically search Claude Code sessions",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&dbPath, "db", "", "path to sessions.db (required)")

	mineCmd := &cobra.Command{
		Use:   "mine <jsonl-path>",
		Short: "Parse a JSONL session and index it",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			emb := embed.NewClient()
			// 50s deadline leaves headroom under the 60s Stop-hook budget;
			// chunks past the deadline are stored but deferred to `embed`.
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			res, err := mine.Run(ctx, dbPath, args[0], emb)
			if err != nil {
				return err
			}
			// No embeddings at all and nothing deferred: either Ollama was
			// unavailable or every Embed call failed. Don't claim a cause we
			// can't confirm — just point at the backfill command.
			if res.Embedded == 0 && res.ChunksInserted > 0 && res.Deferred == 0 {
				fmt.Fprintf(os.Stderr, "warn: indexed without embeddings — run: session-indexer embed --db %s\n", dbPath)
			}
			if res.Deferred > 0 {
				fmt.Fprintf(os.Stderr, "warn: %d chunks deferred (run: session-indexer embed --db %s)\n",
					res.Deferred, dbPath)
			}
			fmt.Printf("mined: %d chunks inserted, %d embedded, %d skipped, %d deferred\n",
				res.ChunksInserted, res.Embedded, res.Skipped, res.Deferred)
			return nil
		},
	}

	var limit int
	var asJSON bool
	searchCmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Semantic search over indexed sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			d, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer d.Close()
			res, used, err := search.Search(d, embed.NewClient(), args[0], limit)
			if err != nil {
				return err
			}
			// Warn when cosine was used but some chunks lack embeddings:
			// those chunks are invisible to search until `embed` backfills them.
			if used {
				if st, e := search.GetStats(d, dbPath); e == nil && st.Pending > 0 {
					fmt.Fprintf(os.Stderr,
						"warn: %d chunks not yet embedded — results may be incomplete; run: session-indexer embed --db %s\n",
						st.Pending, dbPath)
				}
			}
			return printResults(res, used, asJSON)
		},
	}
	searchCmd.Flags().IntVar(&limit, "limit", 5, "max results")
	searchCmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	embedCmd := &cobra.Command{
		Use:   "embed",
		Short: "Backfill embeddings for chunks missing them",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			return runEmbed(dbPath)
		},
	}

	statsCmd := &cobra.Command{
		Use:   "stats",
		Short: "Report index state",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			d, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer d.Close()
			st, err := search.GetStats(d, dbPath)
			if err != nil {
				return err
			}
			fmt.Printf("Sessions indexed: %d\nChunks total:     %d\nWith embeddings:  %d (%d pending)\nOldest entry:     %s\nNewest entry:     %s\nDB size:          %s\n",
				st.Sessions, st.Chunks, st.Embedded, st.Pending, st.Oldest, st.Newest, st.DBSize)
			return nil
		},
	}

	root.AddCommand(mineCmd, searchCmd, embedCmd, statsCmd)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runEmbed(dbPath string) error {
	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()
	emb := embed.NewClient()
	if !emb.Available() {
		return fmt.Errorf("ollama unavailable — start it and pull bge-m3:latest")
	}
	pending, err := db.ChunksWithoutEmbeddings(d)
	if err != nil {
		return err
	}
	var n, failed int
	for _, p := range pending {
		vec, err := emb.Embed(context.Background(), p.Content)
		if err != nil {
			failed++
			continue
		}
		if err := db.InsertEmbedding(d, p.ID, embed.EncodeVector(vec)); err != nil {
			failed++
			continue
		}
		n++
	}
	fmt.Printf("Embedded %d pending chunks.\n", n)
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "warn: %d chunks failed to embed\n", failed)
	}
	return nil
}

func printResults(res []internal.SearchResult, usedEmbeddings, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	if !usedEmbeddings {
		fmt.Println("(embedding unavailable — FTS5 keyword results only)")
	}
	for _, r := range res {
		fmt.Printf("[%s | %s]\n%s\n%s\n", r.SessionDate, r.Role, snippet(r.Content),
			"──────────────────────────────────────────────────────")
	}
	if len(res) == 0 {
		fmt.Println("(no results)")
	}
	return nil
}

func snippet(s string) string {
	const max = 200
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cut := string(runes[:max])
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return cut + "…"
}
