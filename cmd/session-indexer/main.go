package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
	"github.com/valpere/session-indexer/internal/embed"
	"github.com/valpere/session-indexer/internal/mine"
	"github.com/valpere/session-indexer/internal/search"
)

func main() {
	var dbPath string

	root := &cobra.Command{
		Use:           "session-indexer",
		Short:         "Index and semantically search Claude Code sessions",
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
			res, err := mine.Run(dbPath, args[0], emb)
			if err != nil {
				return err
			}
			if res.Embedded == 0 && res.ChunksInserted > 0 {
				fmt.Fprintln(os.Stderr, "warn: ollama unavailable, indexed without embeddings")
			}
			fmt.Printf("mined: %d chunks inserted, %d embedded, %d skipped\n",
				res.ChunksInserted, res.Embedded, res.Skipped)
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
			st, err := search.GetStats(d)
			if err != nil {
				return err
			}
			fmt.Printf("Sessions indexed: %d\nChunks total:     %d\nWith embeddings:  %d (%d pending)\nOldest entry:     %s\nNewest entry:     %s\n",
				st.Sessions, st.Chunks, st.Embedded, st.Pending, st.Oldest, st.Newest)
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
	n := 0
	for _, p := range pending {
		vec, err := emb.Embed(p.Content)
		if err != nil {
			continue
		}
		if err := db.InsertEmbedding(d, p.ID, embed.EncodeVector(vec)); err == nil {
			n++
		}
	}
	fmt.Printf("Embedded %d pending chunks.\n", n)
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
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := lastSpace(cut); i > 0 {
		cut = cut[:i]
	}
	return cut + "…"
}

func lastSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return -1
}
