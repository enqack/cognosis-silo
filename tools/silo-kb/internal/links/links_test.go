package links

import (
	"reflect"
	"testing"

	"silo.local/silo-kb/internal/vault"
)

func TestTargets(t *testing.T) {
	n := &vault.Note{
		Frontmatter: map[string]any{
			"sources": []any{"[[2026-07-07]]", "[[design-notes|the design]]", "  "},
		},
		Body: "See [[concept-a]] and [[concept-a]] again, plus [[concept-b#heading]].",
	}
	got := Targets(n)
	want := []Ref{
		{Name: "2026-07-07", Kind: Source},
		{Name: "design-notes", Kind: Source},
		{Name: "concept-a", Kind: Wikilink},
		{Name: "concept-b", Kind: Wikilink},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Targets()\n got  %+v\n want %+v", got, want)
	}
}

func TestSources(t *testing.T) {
	n := &vault.Note{
		Frontmatter: map[string]any{
			"sources": []any{"[[a]]", "[[a]]", "[[b]]"},
		},
	}
	got := Sources(n)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sources() = %v, want %v", got, want)
	}
}

func TestSourcesMissing(t *testing.T) {
	n := &vault.Note{Frontmatter: map[string]any{}}
	if got := Sources(n); len(got) != 0 {
		t.Errorf("expected no sources, got %v", got)
	}
}
