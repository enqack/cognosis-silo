package compilepass

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeVaultNote lays down a knowledge note under a temp repo's vault.
func writeVaultNote(t *testing.T, repo, relUnderVault, content string) {
	t.Helper()
	abs := filepath.Join(repo, "knowledge-base", relUnderVault)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const seedNote = `---
id: de6d5441-c97e-415f-b5ca-0df850ff0d84
type: concept
confidence: 0.7
maturity: seed
last_reinforced: 2026-07-07 09:00:00
reinforce_count: 0
sources:
  - "[[2026-07-07]]"
---
A working theory.
`

func TestFalsifyRetainsInPlaceWithReason(t *testing.T) {
	repo := t.TempDir()
	writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
	// log.md must exist for the audit append.
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")

	rep, err := Run(repo, Options{
		Falsify: map[string]string{"de6d5441-c97e-415f-b5ca-0df850ff0d84": "contradicted by benchmark X"},
		Now:     time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.Actions) != 1 || rep.Actions[0].Kind != "falsified" {
		t.Fatalf("expected one falsified action, got %+v", rep.Actions)
	}

	// Note stays in place (retained + queryable), NOT moved to archive.
	if _, err := os.Stat(filepath.Join(repo, "knowledge-base/knowledge/archive/falsified/theory.md")); !os.IsNotExist(err) {
		t.Error("falsified note must not be archived — it is retained in place")
	}
	got, err := os.ReadFile(filepath.Join(repo, "knowledge-base/knowledge/concepts/theory.md"))
	if err != nil {
		t.Fatalf("retained note missing: %v", err)
	}
	for _, want := range []string{"status: falsified", "falsified_reason: contradicted by benchmark X", "falsified_at: 2026-07-07 12:00:00"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("retained note missing %q; got:\n%s", want, got)
		}
	}
}

// A falsified note is inert on later runs: it never decays, archives, or
// graduates, and it is not a valid reinforce/graduate target.
func TestFalsifiedNoteIsInert(t *testing.T) {
	repo := t.TempDir()
	id := "de6d5441-c97e-415f-b5ca-0df850ff0d84"
	writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")

	// Falsify it.
	if _, err := Run(repo, Options{
		Falsify: map[string]string{id: "wrong"},
		Now:     time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local),
	}); err != nil {
		t.Fatal(err)
	}

	// A later run, well past the decay window, must not touch it.
	rep, err := Run(repo, Options{
		Now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rep.Actions {
		if a.Note == "theory" {
			t.Errorf("falsified note should be inert, got action %+v", a)
		}
	}

	// It is no longer a valid reinforce target.
	if _, err := Run(repo, Options{
		Reinforce: []string{id},
		Now:       time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local),
	}); err == nil {
		t.Error("expected error reinforcing a falsified (non-live) note")
	}
}

// --supersede records the replacement as a superseded_by wikilink.
func TestFalsifyWithSupersede(t *testing.T) {
	repo := t.TempDir()
	oldID := "de6d5441-c97e-415f-b5ca-0df850ff0d84"
	writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
	writeVaultNote(t, repo, "knowledge/concepts/better.md", stableNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")

	if _, err := Run(repo, Options{
		Falsify:   map[string]string{oldID: "superseded"},
		Supersede: map[string]string{oldID: "3f8e2c1a-9b4d-4e6f-8a2b-1c3d5e7f9a0b"},
		Now:       time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(repo, "knowledge-base/knowledge/concepts/theory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "superseded_by: '[[better]]'") && !strings.Contains(string(got), "superseded_by: \"[[better]]\"") && !strings.Contains(string(got), "superseded_by: [[better]]") {
		t.Errorf("expected superseded_by wikilink to [[better]]; got:\n%s", got)
	}
}

// Reinforcing and falsifying (or disputing) the same note in one run is a
// contradiction: falsify used to silently win, hiding it. Now the whole run is
// rejected so the invoking agent resolves it.
func TestSameRunContradictionRejected(t *testing.T) {
	id := "de6d5441-c97e-415f-b5ca-0df850ff0d84"
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local)

	cases := map[string]Options{
		"reinforce+falsify": {Reinforce: []string{id}, Falsify: map[string]string{id: "wrong"}},
		"reinforce+dispute": {Reinforce: []string{id}, Dispute: map[string]string{id: "unsure"}},
		"falsify+dispute":   {Falsify: map[string]string{id: "wrong"}, Dispute: map[string]string{id: "unsure"}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
			writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")
			opts.Now = now
			_, err := Run(repo, opts)
			if err == nil || !strings.Contains(err.Error(), "contradictory operations") {
				t.Fatalf("expected a contradiction error, got %v", err)
			}
		})
	}
}

// --dispute contests a note without disproving it: it stays live (status
// disputed) with a reason, and is NOT moved or frozen.
func TestDisputeMarksLiveWithReason(t *testing.T) {
	repo := t.TempDir()
	id := "de6d5441-c97e-415f-b5ca-0df850ff0d84"
	writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")

	rep, err := Run(repo, Options{
		Dispute: map[string]string{id: "conflicts with note Y"},
		Now:     time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Actions) != 1 || rep.Actions[0].Kind != "disputed" {
		t.Fatalf("expected one disputed action, got %+v", rep.Actions)
	}
	got, err := os.ReadFile(filepath.Join(repo, "knowledge-base/knowledge/concepts/theory.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"status: disputed", "disputed_reason: conflicts with note Y", "disputed_at: 2026-07-07 12:00:00"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("disputed note missing %q; got:\n%s", want, got)
		}
	}
}

func TestDisputeRequiresReason(t *testing.T) {
	repo := t.TempDir()
	writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")

	_, err := Run(repo, Options{
		Dispute: map[string]string{"de6d5441-c97e-415f-b5ca-0df850ff0d84": "  "},
		Now:     time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local),
	})
	if err == nil {
		t.Fatal("dispute with blank reason should error")
	}
}

// Reinforcing a disputed note (an agent re-asserting it) clears the dispute
// back to active automatically — no human unlock.
func TestReinforceClearsDispute(t *testing.T) {
	repo := t.TempDir()
	id := "de6d5441-c97e-415f-b5ca-0df850ff0d84"
	writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")

	if _, err := Run(repo, Options{
		Dispute: map[string]string{id: "unsure"},
		Now:     time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local),
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(repo, Options{
		Reinforce: []string{id},
		Now:       time.Date(2026, 7, 8, 12, 0, 0, 0, time.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	var cleared bool
	for _, a := range rep.Actions {
		if a.Kind == "dispute-cleared" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("expected a dispute-cleared action, got %+v", rep.Actions)
	}
	got, err := os.ReadFile(filepath.Join(repo, "knowledge-base/knowledge/concepts/theory.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "status: active") {
		t.Errorf("reinforced note should be back to active; got:\n%s", s)
	}
	if strings.Contains(s, "disputed_reason") || strings.Contains(s, "disputed_at") {
		t.Errorf("dispute fields should be removed on clear; got:\n%s", s)
	}
}

const stableNote = `---
id: 3f8e2c1a-9b4d-4e6f-8a2b-1c3d5e7f9a0b
type: concept
confidence: 0.9
maturity: stable
last_reinforced: 2026-07-07 09:00:00
reinforce_count: 4
sources:
  - "[[2026-07-07]]"
---
A stable theory.
`

// A typo'd id/path must fail the whole run up front — silently doing nothing
// would let the agent report work that never happened.
func TestUnmatchedIDsError(t *testing.T) {
	repo := t.TempDir()
	writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation Log\n")

	_, err := Run(repo, Options{
		Reinforce: []string{"no-such-id"},
		Falsify:   map[string]string{"another-typo": "reason"},
		Now:       time.Date(2026, 7, 9, 12, 0, 0, 0, time.Local),
	})
	if err == nil {
		t.Fatal("expected error for unmatched ids")
	}
	for _, want := range []string{"no live knowledge note matches", "--reinforce no-such-id", "--falsify another-typo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q; got:\n%v", want, err)
		}
	}
}

func TestGraduateRejectsFlatDestination(t *testing.T) {
	repo := t.TempDir()
	id := "3f8e2c1a-9b4d-4e6f-8a2b-1c3d5e7f9a0b"
	writeVaultNote(t, repo, "knowledge/concepts/stable.md", stableNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation Log\n")

	_, err := Run(repo, Options{
		Graduate: map[string]string{id: "projects/flat.md"},
		Now:      time.Date(2026, 7, 9, 12, 0, 0, 0, time.Local),
	})
	if err == nil || !strings.Contains(err.Error(), "projects/<name>/<note>.md") {
		t.Fatalf("flat projects/ destination must be rejected (the indexer never sees it), got %v", err)
	}
}

func TestStableCandidatesReported(t *testing.T) {
	repo := t.TempDir()
	writeVaultNote(t, repo, "knowledge/concepts/stable.md", stableNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation Log\n")

	rep, err := Run(repo, Options{Now: time.Date(2026, 7, 9, 12, 0, 0, 0, time.Local)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.StableCandidates) != 1 || rep.StableCandidates[0] != "stable" {
		t.Errorf("expected [stable] as graduation candidates, got %v", rep.StableCandidates)
	}
}

// gitCommitAt commits everything in repo with the given commit date.
func gitCommitAt(t *testing.T, repo, date string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed", "--date", date},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// A note reinforced in this run must not be archived as ancient in the same
// run, however old its last commit; without reinforcement it is archived.
func TestReinforceShieldsFromAncientArchival(t *testing.T) {
	id := "de6d5441-c97e-415f-b5ca-0df850ff0d84"
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.Local)

	setup := func(t *testing.T) string {
		repo := t.TempDir()
		writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
		writeVaultNote(t, repo, "knowledge/log.md", "# Compilation Log\n")
		gitCommitAt(t, repo, "2025-12-01T12:00:00")
		return repo
	}

	t.Run("unreinforced ancient note is archived", func(t *testing.T) {
		repo := setup(t)
		rep, err := Run(repo, Options{Now: now})
		if err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for _, a := range rep.Actions {
			kinds = append(kinds, a.Kind)
		}
		if !strings.Contains(strings.Join(kinds, " "), "archived-ancient") {
			t.Fatalf("expected archived-ancient, got %v", kinds)
		}
	})

	t.Run("reinforced note survives", func(t *testing.T) {
		repo := setup(t)
		rep, err := Run(repo, Options{Reinforce: []string{id}, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range rep.Actions {
			if a.Kind == "archived-ancient" {
				t.Fatalf("reinforced note must not be ancient-archived, got %+v", rep.Actions)
			}
		}
		if _, err := os.Stat(filepath.Join(repo, "knowledge-base/knowledge/concepts/theory.md")); err != nil {
			t.Errorf("reinforced note should still be live: %v", err)
		}
	})
}

func TestFalsifyRequiresReason(t *testing.T) {
	repo := t.TempDir()
	writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
	writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")

	_, err := Run(repo, Options{
		Falsify: map[string]string{"de6d5441-c97e-415f-b5ca-0df850ff0d84": "  "},
		Now:     time.Date(2026, 7, 7, 12, 0, 0, 0, time.Local),
	})
	if err == nil {
		t.Fatal("falsify with blank reason should error")
	}
}

// A daily log citing [[theory]], committed recently, keeps theory from decaying
// even though its last_reinforced is well past the stale window — citation as an
// implicit signal of continued relevance. Confidence is not raised, only held.
const citingDaily = `---
id: 6b1f0c2e-1a2b-4c3d-8e4f-5a6b7c8d9e0f
type: daily-log
title: 2026-08-28
timestamp: 2026-08-28 12:00:00
---
# 2026-08-28

## 12:00:00
### Log
- revisited [[theory]] while debugging
`

func TestPassiveCitationRefresh(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local) // 56 days after last_reinforced

	t.Run("recently cited note is refreshed, not decayed", func(t *testing.T) {
		repo := t.TempDir()
		writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
		writeVaultNote(t, repo, "daily/2026-08-28.md", citingDaily)
		writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")
		gitCommitAt(t, repo, "2026-08-28T12:00:00") // citing file committed 4 days ago

		rep, err := Run(repo, Options{Now: now})
		if err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for _, a := range rep.Actions {
			if a.Note == "theory" {
				kinds = append(kinds, a.Kind)
			}
		}
		if strings.Contains(strings.Join(kinds, " "), "decayed") {
			t.Fatalf("recently-cited note must not decay, got %v", kinds)
		}
		if !strings.Contains(strings.Join(kinds, " "), "refreshed") {
			t.Fatalf("expected a refreshed action, got %v", kinds)
		}
		got, _ := os.ReadFile(filepath.Join(repo, "knowledge-base/knowledge/concepts/theory.md"))
		if !strings.Contains(string(got), "confidence: 0.7") {
			t.Errorf("refresh must not change confidence; got:\n%s", got)
		}
	})

	t.Run("uncited stale note still decays", func(t *testing.T) {
		repo := t.TempDir()
		writeVaultNote(t, repo, "knowledge/concepts/theory.md", seedNote)
		writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")
		gitCommitAt(t, repo, "2026-08-28T12:00:00")

		rep, err := Run(repo, Options{Now: now})
		if err != nil {
			t.Fatal(err)
		}
		var decayed bool
		for _, a := range rep.Actions {
			if a.Note == "theory" && a.Kind == "decayed" {
				decayed = true
			}
		}
		if !decayed {
			t.Fatalf("uncited stale note should decay, got %+v", rep.Actions)
		}
	})
}

const pausedStableNote = `---
id: 7c2e1d3f-2b3c-4d5e-9f6a-7b8c9d0e1f2a
type: concept
confidence: 0.9
maturity: stable
last_reinforced: 2026-07-07 09:00:00
reinforce_count: 4
status: paused
sources:
  - "[[2026-07-07]]"
---
Blocked on a vendor fix.
`

// A paused note's decay clock is suspended: it neither decays nor counts as a
// graduation candidate, and cannot be graduated until unpaused.
func TestPausedNoteSuspendedFromLifecycle(t *testing.T) {
	id := "7c2e1d3f-2b3c-4d5e-9f6a-7b8c9d0e1f2a"
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)

	setup := func(t *testing.T) string {
		repo := t.TempDir()
		writeVaultNote(t, repo, "knowledge/concepts/blocked.md", pausedStableNote)
		writeVaultNote(t, repo, "knowledge/log.md", "# Compilation log\n")
		gitCommitAt(t, repo, "2026-08-28T12:00:00")
		return repo
	}

	t.Run("does not decay and is not a graduation candidate", func(t *testing.T) {
		repo := setup(t)
		rep, err := Run(repo, Options{Now: now})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range rep.Actions {
			if a.Note == "blocked" && a.Kind == "decayed" {
				t.Fatalf("paused note must not decay, got %+v", rep.Actions)
			}
		}
		for _, c := range rep.StableCandidates {
			if c == "blocked" {
				t.Fatal("paused note must not be a graduation candidate")
			}
		}
		got, _ := os.ReadFile(filepath.Join(repo, "knowledge-base/knowledge/concepts/blocked.md"))
		if !strings.Contains(string(got), "confidence: 0.9") {
			t.Errorf("paused note confidence must be unchanged; got:\n%s", got)
		}
	})

	t.Run("cannot be graduated while paused", func(t *testing.T) {
		repo := setup(t)
		_, err := Run(repo, Options{
			Graduate: map[string]string{id: "projects/demo/blocked.md"},
			Now:      now,
		})
		if err == nil || !strings.Contains(err.Error(), "paused") {
			t.Fatalf("expected refusal to graduate a paused note, got %v", err)
		}
	})
}
