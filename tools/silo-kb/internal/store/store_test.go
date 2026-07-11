package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSockDirShortPathIsPgdata(t *testing.T) {
	got, err := sockDir("/repo", "/repo/.pg-data")
	if err != nil || got != "/repo/.pg-data" {
		t.Errorf("short path should use .pg-data directly, got (%q, %v)", got, err)
	}
}

func TestSockDirPrefersPersistedSocketPath(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".pg-socket-path"), []byte("/tmp/silokb-abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(repo, strings.Repeat("d", 100), ".pg-data")
	got, err := sockDir(repo, long)
	if err != nil || got != "/tmp/silokb-abc" {
		t.Errorf("persisted .pg-socket-path must win, got (%q, %v)", got, err)
	}
}

// The recomputed fallback must produce the same directory pg-start persists:
// sha1 of the repo path, first 12 hex chars — verified against the exact
// pipeline flake.nix runs (`echo -n "$PWD" | shasum | cut -c1-12`).
func TestSockDirHashMatchesFlake(t *testing.T) {
	if _, err := exec.LookPath("shasum"); err != nil {
		t.Skip("shasum not available")
	}
	repo := t.TempDir() // no .pg-socket-path → recompute
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	long := filepath.Join(repo, strings.Repeat("d", 100), ".pg-data")
	got, err := sockDir(repo, long)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("shasum")
	cmd.Stdin = strings.NewReader(repo)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, "silokb-"+string(out[:12]))
	if got != want {
		t.Errorf("Go fallback %q != flake pipeline %q", got, want)
	}
}
