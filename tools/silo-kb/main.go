// silo-kb maintains the derived Postgres index over knowledge-base/ and runs
// the knowledge lifecycle. The markdown tree is the single source of truth;
// everything here is derived and droppable.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// findRepoRoot walks up from cwd to the directory containing knowledge-base/.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if st, err := os.Stat(filepath.Join(dir, "knowledge-base")); err == nil && st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("knowledge-base/ not found above %s", dir)
		}
		dir = parent
	}
}

func main() {
	root := &cobra.Command{
		Use:           "silo-kb",
		Short:         "Derived-index and knowledge-lifecycle tooling for the silo knowledge base",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		migrateCmd(),
		reindexCmd(),
		resetCmd(),
		queryCmd(),
		compileCmd(),
		syncIndexCmd(),
		injectIndexCmd(),
		validateCmd(),
		serveMCPCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
