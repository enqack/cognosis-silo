package indexgen

import (
	"testing"

	"silo.local/silo-kb/internal/vault"
)

// A time-primary daily log: two `## <time>` blocks, each with `### <Category>`
// subsections. Counts must aggregate per category across blocks, and the
// `## <time>` headings must not be mistaken for categories.
const timePrimaryDaily = `# 2026-07-07

## 09:00:00

### Concepts
- Postgres as a derived index.
- Two-tier vault.

### Log
- Drafted the architecture.

## 14:02:00

### Concepts
- Time-primary daily structure.

### Cursed Knowledge
- mtime is meaningless after a git checkout.

### Unresolved
- [ ] Decide the vault-sharing model.
`

func TestCategoryCountsAggregatesAcrossTimeBlocks(t *testing.T) {
	got := categoryCounts(timePrimaryDaily)

	want := map[string]int{
		"Concepts":         3, // 2 in the 09:00 block + 1 in the 14:02 block
		"Log":              1,
		"Cursed Knowledge": 1,
		"Unresolved":       1,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d categories, want %d: %#v", len(got), len(want), got)
	}
	for _, c := range got {
		if want[c.category] != c.items {
			t.Errorf("category %q: got %d items, want %d", c.category, c.items, want[c.category])
		}
	}
	// First-seen order: Concepts appears before Log.
	if got[0].category != "Concepts" || got[1].category != "Log" {
		t.Errorf("first-seen order not preserved: %#v", got)
	}
}

func TestDailyLineDigest(t *testing.T) {
	n := &vault.Note{Path: "knowledge-base/daily/2026-07-07.md", Body: timePrimaryDaily}
	got := dailyLine(n)

	// Unresolved is reported via the checkbox count, not the category count.
	want := "- [[2026-07-07]] `daily` — 3 concepts, 1 log, 1 cursed knowledge, 1 unresolved"
	if got != want {
		t.Errorf("dailyLine =\n  %q\nwant\n  %q", got, want)
	}
}
