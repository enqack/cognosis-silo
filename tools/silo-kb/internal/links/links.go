// Package links extracts the outbound links a note declares: `sources`
// frontmatter (provenance to daily/deep-thought captures) and body wikilinks.
// It is markdown-only parsing — resolution of a basename to a note id, and
// persistence, are the caller's job (store.Reindex).
package links

import (
	"regexp"
	"strings"

	"silo.local/silo-kb/internal/vault"
)

// Kind is the relationship a link records. Values match the `links.kind` column.
type Kind string

const (
	Wikilink Kind = "wikilink" // a [[...]] reference in the note body
	Source   Kind = "source"   // a `sources` frontmatter entry (provenance)
)

// Ref is one outbound link: the target's wikilink basename and how it was
// declared.
type Ref struct {
	Name string
	Kind Kind
}

var wikiRe = regexp.MustCompile(`\[\[([^\]\[]+)\]\]`)

// Targets returns a note's outbound links, de-duplicated by (name, kind):
// `sources` entries first (kind=source), then body wikilinks (kind=wikilink).
// Empty/unparseable names are skipped.
func Targets(n *vault.Note) []Ref {
	var out []Ref
	seen := map[string]bool{}
	add := func(name string, k Kind) {
		name = cleanName(name)
		if name == "" {
			return
		}
		key := string(k) + "\x00" + name
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Ref{Name: name, Kind: k})
	}

	if srcs, ok := n.Frontmatter["sources"].([]any); ok {
		for _, s := range srcs {
			if str, ok := s.(string); ok {
				add(str, Source)
			}
		}
	}
	for _, m := range wikiRe.FindAllStringSubmatch(n.Body, -1) {
		add(m[1], Wikilink)
	}
	return out
}

// Sources returns just the provenance targets (basenames) a note declares —
// the resolvable half of the `sources` contract.
func Sources(n *vault.Note) []string {
	var out []string
	seen := map[string]bool{}
	if srcs, ok := n.Frontmatter["sources"].([]any); ok {
		for _, s := range srcs {
			str, ok := s.(string)
			if !ok {
				continue
			}
			name := cleanName(str)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// cleanName normalizes a wikilink target to a bare basename: strips [[ ]]
// delimiters, an optional |alias or #anchor, and surrounding whitespace.
func cleanName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[[")
	s = strings.TrimSuffix(s, "]]")
	if i := strings.IndexAny(s, "|#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
