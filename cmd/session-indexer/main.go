package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/valpere/session-indexer/internal"
	"github.com/valpere/session-indexer/internal/db"
	"github.com/valpere/session-indexer/internal/distill"
	"github.com/valpere/session-indexer/internal/embed"
	"github.com/valpere/session-indexer/internal/facts"
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
			fmt.Printf("Sessions indexed: %d\nChunks total:     %d\nWith embeddings:  %d (%d pending)\nFacts current:    %d\nPending distill:  %d\nOldest entry:     %s\nNewest entry:     %s\nDB size:          %s\n",
				st.Sessions, st.Chunks, st.Embedded, st.Pending, st.Facts, st.PendingDistill, st.Oldest, st.Newest, st.DBSize)
			return nil
		},
	}

	var threshold float64
	var distillModel string
	distillCmd := &cobra.Command{
		Use:   "distill",
		Short: "Extract structured facts from mined chunks (LLM, Ollama)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			return runDistill(dbPath, threshold, distillModel)
		},
	}
	distillCmd.Flags().Float64Var(&threshold, "threshold", 0.7, "minimum confidence to store a fact")
	distillCmd.Flags().StringVar(&distillModel, "model", "", "Ollama chat/generate model (default: $OLLAMA_DISTILL_MODEL or glm-5.2:cloud)")

	var factsLimit int
	var factsJSON bool
	var includeExpired bool
	factsSearchCmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Keyword search over distilled facts",
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
			res, err := facts.Search(d, args[0], factsLimit, includeExpired)
			if err != nil {
				return err
			}
			return printFacts(res, factsJSON)
		},
	}
	factsSearchCmd.Flags().IntVar(&factsLimit, "limit", 5, "max results")
	factsSearchCmd.Flags().BoolVar(&factsJSON, "json", false, "machine-readable output")
	factsSearchCmd.Flags().BoolVar(&includeExpired, "include-expired", false, "include tombstoned facts")

	var factsGetJSON bool
	factsGetCmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Show a fact and its supersedes edges",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid fact id %q: %w", args[0], err)
			}
			d, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer d.Close()
			fact, incoming, outgoing, err := facts.Get(d, id)
			if err != nil {
				return err
			}
			return printFactDetail(fact, incoming, outgoing, factsGetJSON)
		},
	}
	factsGetCmd.Flags().BoolVar(&factsGetJSON, "json", false, "machine-readable output")

	var factsRelatedJSON bool
	factsRelatedCmd := &cobra.Command{
		Use:   "related <id>",
		Short: "List facts depth-1 related via supersedes",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid fact id %q: %w", args[0], err)
			}
			d, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer d.Close()
			res, err := facts.Related(d, id)
			if err != nil {
				return err
			}
			return printFacts(res, factsRelatedJSON)
		},
	}
	factsRelatedCmd.Flags().BoolVar(&factsRelatedJSON, "json", false, "machine-readable output")

	factsSupersedeCmd := &cobra.Command{
		Use:   "supersede <new-id> <old-id>",
		Short: "Manually tombstone old-id in favor of new-id (audit/override backstop)",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			newID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid new-id %q: %w", args[0], err)
			}
			oldID, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid old-id %q: %w", args[1], err)
			}
			d, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer d.Close()
			changed, err := db.SupersedeFact(d, newID, oldID, time.Now().UTC().Format(time.RFC3339))
			if err != nil {
				return err
			}
			if !changed {
				fmt.Printf("fact %d was already tombstoned; no change made\n", oldID)
				return nil
			}
			fmt.Printf("fact %d superseded by %d\n", oldID, newID)
			return nil
		},
	}

	factsCmd := &cobra.Command{Use: "facts", Short: "Query the distilled facts layer"}
	factsCmd.AddCommand(factsSearchCmd, factsGetCmd, factsRelatedCmd, factsSupersedeCmd)

	root.AddCommand(mineCmd, searchCmd, embedCmd, statsCmd, distillCmd, factsCmd)
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

func runDistill(dbPath string, threshold float64, model string) error {
	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()
	cli := distill.NewClientWithModel(model)
	if !cli.Available() {
		return fmt.Errorf("ollama unavailable — start it and pull the distill model (OLLAMA_DISTILL_MODEL)")
	}
	// distill is a manual, non-time-boxed command, explicitly exempt from
	// mine's 50s/Stop-hook budget (NFR-4).
	res, err := distill.Run(context.Background(), d, cli, distill.Config{Threshold: threshold, ContextCap: 200})
	if err != nil {
		return err
	}
	fmt.Printf("Distilled %d chunks: %d facts stored, %d below threshold, %d superseded\n",
		res.ChunksDistilled, res.FactsInserted, res.BelowThreshold, res.Superseded)
	if res.Failed > 0 {
		fmt.Fprintf(os.Stderr, "warn: %d chunks failed to distill\n", res.Failed)
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

func printFacts(res []internal.Fact, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	for _, f := range res {
		fmt.Printf("[%d] %s | %s | %s (confidence %.2f)%s\n",
			f.ID, f.Subject, f.Predicate, f.Object, f.Confidence, factStatusSuffix(f))
	}
	if len(res) == 0 {
		fmt.Println("(no results)")
	}
	return nil
}

func printFactDetail(fact internal.Fact, incoming, outgoing []internal.Fact, asJSON bool) error {
	if asJSON {
		out := struct {
			Fact     internal.Fact   `json:"fact"`
			Incoming []internal.Fact `json:"incoming"`
			Outgoing []internal.Fact `json:"outgoing"`
		}{fact, incoming, outgoing}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Printf("[%d] %s | %s | %s (confidence %.2f)%s\n",
		fact.ID, fact.Subject, fact.Predicate, fact.Object, fact.Confidence, factStatusSuffix(fact))
	if len(incoming) > 0 {
		fmt.Println("Superseded by this fact:")
		for _, f := range incoming {
			fmt.Printf("  [%d] %s | %s | %s\n", f.ID, f.Subject, f.Predicate, f.Object)
		}
	}
	if len(outgoing) > 0 {
		fmt.Println("Superseded by:")
		for _, f := range outgoing {
			fmt.Printf("  [%d] %s | %s | %s\n", f.ID, f.Subject, f.Predicate, f.Object)
		}
	}
	return nil
}

func factStatusSuffix(f internal.Fact) string {
	if f.Until == nil {
		return ""
	}
	if f.SupersededBy != nil {
		return fmt.Sprintf(" [tombstoned %s, superseded by %d]", *f.Until, *f.SupersededBy)
	}
	return fmt.Sprintf(" [tombstoned %s]", *f.Until)
}
