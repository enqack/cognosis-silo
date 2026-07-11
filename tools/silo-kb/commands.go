package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"silo.local/silo-kb/internal/compilepass"
	"silo.local/silo-kb/internal/embed"
	"silo.local/silo-kb/internal/indexgen"
	"silo.local/silo-kb/internal/links"
	"silo.local/silo-kb/internal/mcpserver"
	"silo.local/silo-kb/internal/query"
	"silo.local/silo-kb/internal/scaffold"
	"silo.local/silo-kb/internal/store"
	"silo.local/silo-kb/internal/validate"
	"silo.local/silo-kb/internal/vault"
)

func vaultRoot() (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "knowledge-base"), nil
}

func migrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply the derived-index schema idempotently",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pool, err := store.Connect(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := store.Migrate(ctx, pool); err != nil {
				return err
			}
			fmt.Println("schema up to date")
			return nil
		},
	}
}

func reindexCmd() *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Delta-sync the vault into Postgres (chunks + embeddings)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vr, err := vaultRoot()
			if err != nil {
				return err
			}
			notes, err := vault.Walk(vr)
			if err != nil {
				return err
			}
			pool, err := store.Connect(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()
			stats, err := store.Reindex(ctx, pool, embed.New(""), notes, full)
			if err != nil {
				return err
			}
			fmt.Printf("notes: %d (%d unchanged, %d moved, %d pruned)\nchunks: %d embedded, %d unchanged\nlinks: %d\n",
				stats.Notes, stats.SkippedNotes, stats.MovedNotes, stats.NotesPruned,
				stats.ChunksEmbedded, stats.ChunksKept, stats.Links)
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "truncate the index and rebuild everything")
	return cmd
}

func resetCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Wipe the vault back to the fresh-silo scaffold and rebuild the index",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("reset deletes every note in knowledge-base/ — re-run with --force (git is your recovery net)")
			}
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			vr := filepath.Join(root, "knowledge-base")
			if err := os.RemoveAll(vr); err != nil {
				return fmt.Errorf("wiping %s: %w", vr, err)
			}
			if err := scaffold.Write(root); err != nil {
				return err
			}
			fmt.Println("vault reset to the fresh-silo scaffold")

			// Rebuild the derived index so Postgres matches the now-empty tree.
			// The scaffold has no walkable notes, so this just truncates. If the
			// DB is unreachable the wipe still stands — report and exit clean.
			ctx := cmd.Context()
			pool, err := store.Connect(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vault reset, but index not rebuilt (%v) — run: pg-start && silo-kb reindex --full\n", err)
				return nil
			}
			defer pool.Close()
			notes, err := vault.Walk(vr)
			if err != nil {
				return err
			}
			if _, err := store.Reindex(ctx, pool, embed.New(""), notes, true); err != nil {
				fmt.Fprintf(os.Stderr, "vault reset, but reindex failed (%v) — run: silo-kb reindex --full\n", err)
				return nil
			}
			if err := indexgen.Write(vr); err != nil {
				return err
			}
			fmt.Println("index rebuilt; knowledge/index.md regenerated")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required — confirm wiping the vault back to the scaffold")
	return cmd
}

func queryCmd() *cobra.Command {
	var project string
	var topK int
	var includeFalsified bool
	cmd := &cobra.Command{
		Use:   "query <text>",
		Short: "Hybrid RRF retrieval over the full corpus",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pool, err := store.Connect(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()
			vec, err := embed.New("").Query(ctx, args[0])
			if err != nil {
				return err
			}
			results, err := query.Run(ctx, pool, vec, args[0], project, topK, includeFalsified)
			if err != nil {
				return err
			}
			fmt.Println(mcpserver.Format(results))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "restrict to one project")
	cmd.Flags().IntVar(&topK, "top-k", query.DefaultTopK, "number of fused results")
	cmd.Flags().BoolVar(&includeFalsified, "include-falsified", false, "include retained-but-invalidated (falsified) notes in results")
	return cmd
}

func compileCmd() *cobra.Command {
	var reinforce []string
	var graduate []string
	var falsify []string
	var dispute []string
	var supersede []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Run the knowledge lifecycle: reinforce, decay, archive, falsify, graduate",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			grad := map[string]string{}
			for _, g := range graduate {
				k, v, ok := strings.Cut(g, ":")
				if !ok {
					return fmt.Errorf("--graduate wants <id-or-path>:<projects/...>, got %q", g)
				}
				grad[k] = v
			}
			fals := map[string]string{}
			for _, f := range falsify {
				k, v, ok := strings.Cut(f, "=")
				if !ok || strings.TrimSpace(v) == "" {
					return fmt.Errorf("--falsify wants <id-or-path>=<reason>, got %q", f)
				}
				fals[k] = v
			}
			disp := map[string]string{}
			for _, d := range dispute {
				k, v, ok := strings.Cut(d, "=")
				if !ok || strings.TrimSpace(v) == "" {
					return fmt.Errorf("--dispute wants <id-or-path>=<reason>, got %q", d)
				}
				disp[k] = v
			}
			sup := map[string]string{}
			for _, s := range supersede {
				k, v, ok := strings.Cut(s, ":")
				if !ok || strings.TrimSpace(v) == "" {
					return fmt.Errorf("--supersede wants <falsified-id-or-path>:<replacement-id-or-path>, got %q", s)
				}
				sup[k] = v
			}
			report, err := compilepass.Run(root, compilepass.Options{
				Reinforce: reinforce,
				Graduate:  grad,
				Falsify:   fals,
				Dispute:   disp,
				Supersede: sup,
				DryRun:    dryRun,
			})
			if err != nil {
				return err
			}
			fmt.Print(report.String())
			if dryRun {
				fmt.Println("(dry run — nothing written)")
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&reinforce, "reinforce", nil, "note ids or vault-relative paths confirmed by this run (comma-separated or repeated)")
	// StringArrayVar, not StringSliceVar: destinations and reasons are free
	// text — a comma inside a reason must not split the value. Repeat the flag
	// for multiple notes.
	cmd.Flags().StringArrayVar(&graduate, "graduate", nil, "<id-or-path>:<projects/name/dest.md> — move a stable article to canon (repeatable)")
	cmd.Flags().StringArrayVar(&falsify, "falsify", nil, "<id-or-path>=<reason> — invalidate a theory determined false in place (retained + queryable, records dissent not decay; repeatable)")
	cmd.Flags().StringArrayVar(&dispute, "dispute", nil, "<id-or-path>=<reason> — contest a theory without disproving it (stays live and decaying; a later reinforce clears it; repeatable)")
	cmd.Flags().StringArrayVar(&supersede, "supersede", nil, "<falsified-id-or-path>:<replacement-id-or-path> — record what replaced a note falsified this run (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report without writing")
	return cmd
}

func syncIndexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync-index",
		Short: "Regenerate knowledge-base/knowledge/index.md",
		RunE: func(cmd *cobra.Command, args []string) error {
			vr, err := vaultRoot()
			if err != nil {
				return err
			}
			if err := indexgen.Write(vr); err != nil {
				return err
			}
			fmt.Println("knowledge/index.md regenerated")
			return nil
		},
	}
}

func injectIndexCmd() *cobra.Command {
	var budget int
	cmd := &cobra.Command{
		Use:   "inject-index",
		Short: "Emit the index truncated to a context budget (for SessionStart hooks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			vr, err := vaultRoot()
			if err != nil {
				return err
			}
			content, err := indexgen.Generate(vr)
			if err != nil {
				return err
			}
			fmt.Println(indexgen.Truncate(content, budget))
			return nil
		},
	}
	cmd.Flags().IntVar(&budget, "budget", 4000, "character budget (0 = unlimited)")
	return cmd
}

func serveMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve-mcp",
		Short: "Serve query_knowledge over stdio MCP",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcpserver.Run(cmd.Context())
		},
	}
}

// --- validate: CLI mode and Claude Code PreToolUse hook mode ---

func validateCmd() *cobra.Command {
	var hookStdin bool
	cmd := &cobra.Command{
		Use:   "validate [files...]",
		Short: "Validate vault frontmatter contracts",
		RunE: func(cmd *cobra.Command, args []string) error {
			if hookStdin {
				runHook() // handles its own exit codes; never returns an error
				return nil
			}
			vr, err := vaultRoot()
			if err != nil {
				return err
			}
			notes, err := vault.Walk(vr)
			if err != nil {
				return err
			}
			// Whole-vault provenance check: a knowledge note's `sources` must
			// resolve to real captures. This needs the full note set, so it lives
			// here rather than in the per-note validate.Note (and the PreToolUse
			// hook, which sees one file at a time).
			if errs := validateProvenance(notes); len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintln(os.Stderr, e)
				}
				return fmt.Errorf("%d unresolved provenance source(s)", len(errs))
			}
			fmt.Println("vault valid")
			return nil
		},
	}
	cmd.Flags().BoolVar(&hookStdin, "hook-stdin", false, "read a Claude Code PreToolUse hook payload from stdin")
	return cmd
}

// validateProvenance enforces that every live knowledge note's `sources`
// entries resolve to an existing daily/ or deep-thought capture. Provenance is
// part of the contract: an unresolved source is a hard failure, not a warning.
func validateProvenance(notes []*vault.Note) []string {
	captures := map[string]bool{}
	for _, n := range notes {
		if n.Tier == vault.TierDaily || n.Tier == vault.TierDeepThought {
			captures[strings.TrimSuffix(filepath.Base(n.Path), ".md")] = true
		}
	}
	var errs []string
	for _, n := range notes {
		if n.Tier != vault.TierKnowledge || strings.HasPrefix(n.Path, "knowledge/archive/") {
			continue
		}
		for _, src := range links.Sources(n) {
			if !captures[src] {
				errs = append(errs, fmt.Sprintf("%s: sources entry [[%s]] does not resolve to an existing daily/ or deep-thought note", n.Path, src))
			}
		}
	}
	return errs
}

// hookInput is the subset of the Claude Code PreToolUse payload we need.
type hookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath   string     `json:"file_path"`
		Content    string     `json:"content"`
		OldString  string     `json:"old_string"`
		NewString  string     `json:"new_string"`
		ReplaceAll bool       `json:"replace_all"`
		Edits      []hookEdit `json:"edits"` // MultiEdit
	} `json:"tool_input"`
	CWD string `json:"cwd"`
}

type hookEdit struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// applyEdit mirrors the Edit tool's semantics on the current file content.
func applyEdit(current string, e hookEdit) string {
	if e.OldString == "" {
		return current
	}
	if e.ReplaceAll {
		return strings.ReplaceAll(current, e.OldString, e.NewString)
	}
	return strings.Replace(current, e.OldString, e.NewString, 1)
}

// runHook validates a pending Write/Edit against the frontmatter contract.
// Exit 0 allows, exit 2 blocks with stderr fed back to the model. Internal
// errors fail open (exit 0) so a broken validator never wedges unrelated
// writes.
func runHook() {
	var in hookInput
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		os.Exit(0)
	}
	path := in.ToolInput.FilePath
	if path == "" || !strings.HasSuffix(path, ".md") {
		os.Exit(0)
	}

	// Resolve the vault-relative path; only knowledge/ and projects/ are guarded.
	idx := strings.Index(filepath.ToSlash(path), "knowledge-base/")
	if idx < 0 {
		os.Exit(0)
	}
	rel := filepath.ToSlash(path)[idx+len("knowledge-base/"):]
	if !strings.HasPrefix(rel, "knowledge/") && !strings.HasPrefix(rel, "projects/") {
		os.Exit(0)
	}

	// The generated/append-only files are off-limits regardless of content.
	if rel == "knowledge/index.md" {
		fmt.Fprintln(os.Stderr, "knowledge-base/knowledge/index.md is generated — do not hand-edit it; run /kb-sync-index (silo-kb sync-index) instead")
		os.Exit(2)
	}
	if rel == "knowledge/log.md" {
		fmt.Fprintln(os.Stderr, "knowledge-base/knowledge/log.md is the compilation audit trail, appended only by silo-kb compile — do not hand-edit it")
		os.Exit(2)
	}

	// Reconstruct the resulting file content.
	var content []byte
	switch in.ToolName {
	case "Write":
		content = []byte(in.ToolInput.Content)
	case "Edit":
		current, err := os.ReadFile(path)
		if err != nil {
			os.Exit(0) // new file via Edit shouldn't happen; fail open
		}
		if in.ToolInput.OldString == "" {
			os.Exit(0)
		}
		content = []byte(applyEdit(string(current), hookEdit{
			OldString:  in.ToolInput.OldString,
			NewString:  in.ToolInput.NewString,
			ReplaceAll: in.ToolInput.ReplaceAll,
		}))
	case "MultiEdit":
		current, err := os.ReadFile(path)
		if err != nil {
			os.Exit(0)
		}
		if len(in.ToolInput.Edits) == 0 {
			os.Exit(0)
		}
		s := string(current)
		for _, e := range in.ToolInput.Edits {
			s = applyEdit(s, e)
		}
		content = []byte(s)
	default:
		os.Exit(0)
	}

	n, err := vault.ParseNote(rel, content)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid YAML frontmatter: %v — fix the YAML and retry\n", err)
		os.Exit(2)
	}
	if errs := validate.Note(rel, n.Frontmatter, n.Frontmatter != nil); len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "frontmatter contract violation in %s:\n", rel)
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		os.Exit(2)
	}
	os.Exit(0)
}
