package chunk

import (
	"strings"
	"testing"

	"silo.local/silo-kb/internal/vault"
)

func note(tier vault.Tier, body string) *vault.Note {
	return &vault.Note{Path: "test.md", Tier: tier, Body: body}
}

func TestDeepThoughtSingleChunk(t *testing.T) {
	chunks := Split(note(vault.TierDeepThought, "## Looks like a heading\n\nBut stays one chunk.\n"))
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if chunks[0].HeadingPath != "" {
		t.Errorf("deep-thought heading path should be empty, got %q", chunks[0].HeadingPath)
	}
}

func TestDeepThoughtEmbedsDescriptionNotBody(t *testing.T) {
	n := &vault.Note{
		Path: "dt.md",
		Tier: vault.TierDeepThought,
		Frontmatter: map[string]any{
			"description": "Refactored the compile lifecycle to add passive citation refresh.",
		},
		Body: "> If you drop your keys into a river of molten lava, let 'em go.\n",
	}
	chunks := Split(n)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "passive citation refresh") {
		t.Errorf("deep-thought should embed the factual description, got %q", chunks[0].Content)
	}
	if strings.Contains(chunks[0].Content, "molten lava") {
		t.Errorf("comedic body must not be embedded, got %q", chunks[0].Content)
	}
}

func TestH2SplitWithHeadingPaths(t *testing.T) {
	body := `# Title

Preamble that is long enough to stand on its own as a chunk so it does not get
merged away by the small-chunk folding logic. It keeps going for a while to be
safe, well past the two hundred character merge threshold used by the chunker.

## Alpha

Alpha content that is also comfortably long enough to avoid being merged into
its neighbor. Padding padding padding, more useful words about the alpha
section and its many virtues, until we pass two hundred characters again.

## Beta

Beta content, similarly padded to well over the merge threshold. This section
discusses beta things at length, with enough characters that the merge logic
leaves it alone and it stays a standalone chunk in the output.
`
	chunks := Split(note(vault.TierKnowledge, body))
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].HeadingPath != "Title" {
		t.Errorf("preamble heading path = %q, want Title", chunks[0].HeadingPath)
	}
	if chunks[1].HeadingPath != "Title > Alpha" || chunks[2].HeadingPath != "Title > Beta" {
		t.Errorf("heading paths = %q, %q", chunks[1].HeadingPath, chunks[2].HeadingPath)
	}
	for i, c := range chunks {
		if c.Ordinal != i {
			t.Errorf("chunk %d ordinal = %d", i, c.Ordinal)
		}
		if c.Hash == "" {
			t.Errorf("chunk %d missing hash", i)
		}
	}
}

func TestCodeFenceHashNotAHeading(t *testing.T) {
	body := "# T\n\nSome intro long enough not to merge. " + strings.Repeat("pad ", 60) + `

## Real

Content with a code fence:

` + "```bash\n## not a heading\n# also not\n```\n" + strings.Repeat("pad ", 60) + "\n"
	chunks := Split(note(vault.TierKnowledge, body))
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks (fence contents must not split), got %d", len(chunks))
	}
	if !strings.Contains(chunks[1].Content, "## not a heading") {
		t.Error("code fence content missing from chunk")
	}
}

func TestSmallSectionsMerge(t *testing.T) {
	body := "# T\n\n" + strings.Repeat("intro ", 50) + "\n\n## Tiny\n\nshort.\n\n## AlsoTiny\n\nbrief.\n"
	chunks := Split(note(vault.TierKnowledge, body))
	if len(chunks) != 1 {
		t.Fatalf("want 1 merged chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "short.") || !strings.Contains(chunks[0].Content, "brief.") {
		t.Error("merged chunk missing small section content")
	}
}

func TestHardSplitOversized(t *testing.T) {
	para := strings.Repeat("word ", 300) // ~1500 chars
	body := "## Big\n\n" + strings.Repeat(para+"\n\n", 6)
	chunks := Split(note(vault.TierKnowledge, body))
	if len(chunks) < 2 {
		t.Fatalf("oversized section should hard-split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if len(c.Content) > hardSplitOver {
			t.Errorf("chunk %d still oversized: %d chars", i, len(c.Content))
		}
		if c.HeadingPath != "Big" {
			t.Errorf("chunk %d lost heading path: %q", i, c.HeadingPath)
		}
	}
}

func TestAppendOnlyStableHashes(t *testing.T) {
	base := "# 2026-07-07\n\n## Concepts\n\n" + strings.Repeat("concept notes ", 20) +
		"\n\n## Log\n\n" + strings.Repeat("log lines ", 20) + "\n"
	appended := base + "\n## Unresolved\n\n" + strings.Repeat("open question ", 20) + "\n"

	before := Split(note(vault.TierDaily, base))
	after := Split(note(vault.TierDaily, appended))
	if len(after) != len(before)+1 {
		t.Fatalf("append should add exactly one chunk: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Hash != after[i].Hash {
			t.Errorf("chunk %d hash changed on append — delta reindex would re-embed it", i)
		}
	}
}
