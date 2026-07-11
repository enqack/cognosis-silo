// Package chunk splits notes into embeddable chunks. Granularity is
// tier-specific: knowledge/* and projects/* split at h2 boundaries, daily logs
// split at their `## HH:MM:SS` capture blocks (the h2s of the time-primary
// format; categories are h3 subsections inside them), deep-thoughts are one
// chunk per file.
// Chunk identity is positional (Ordinal) so duplicate headings can't corrupt
// the delta diff; content hashes make the delta reindex cheap for append-only
// daily logs.
package chunk

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"silo.local/silo-kb/internal/vault"
)

const (
	mergeBelow    = 200  // chunks shorter than this merge into the previous one
	hardSplitOver = 6000 // chunks longer than this split at paragraph boundaries
)

type Chunk struct {
	Ordinal     int
	HeadingPath string // "" for single-chunk notes (stored as NULL)
	Content     string
	Hash        string
}

// Split chunks a note according to its tier.
func Split(n *vault.Note) []Chunk {
	var raw []section
	switch n.Tier {
	case vault.TierDeepThought:
		// A deep-thought's body is a comedic blockquote whose imagery (molten lava,
		// Chihuahuas) would embed literally and scatter the note across the vector
		// space. Embed the dry factual `description` instead — the validator
		// requires it — so the note clusters near the technical events it documents.
		// The joke stays in the body for humans, unindexed. Fall back to the body
		// only if a description is somehow absent (validation would have blocked it).
		text := ""
		if d, ok := n.Frontmatter["description"].(string); ok {
			text = strings.TrimSpace(d)
		}
		if text == "" {
			text = strings.TrimSpace(n.Body)
		}
		raw = []section{{content: text}}
	default:
		raw = splitAtH2(n.Body)
	}

	merged := mergeSmall(raw)
	var out []Chunk
	for _, s := range merged {
		for _, piece := range hardSplit(s.content) {
			out = append(out, Chunk{
				Ordinal:     len(out),
				HeadingPath: s.headingPath,
				Content:     piece,
				Hash:        hash(piece),
			})
		}
	}
	return out
}

type section struct {
	headingPath string
	content     string
}

var md = goldmark.New()

// splitAtH2 slices the raw source at every level-2 heading found via the
// goldmark AST (robust against "#" inside fenced code blocks, setext headings,
// etc.). The h1 title and any preamble stay with the first chunk.
func splitAtH2(body string) []section {
	src := []byte(body)
	doc := md.Parser().Parse(text.NewReader(src))

	type h2 struct {
		offset int
		title  string
	}
	var h1Title string
	var h2s []h2

	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		h, ok := c.(*ast.Heading)
		if !ok || h.Lines().Len() == 0 {
			continue
		}
		title := string(h.Text(src)) //nolint:staticcheck // fine for plain heading text
		switch h.Level {
		case 1:
			if h1Title == "" {
				h1Title = title
			}
		case 2:
			seg := h.Lines().At(0)
			// Back up from the heading text to the start of its line
			// (covers "## " prefixes; ATX headings are single-line).
			start := seg.Start
			for start > 0 && src[start-1] != '\n' {
				start--
			}
			h2s = append(h2s, h2{offset: start, title: title})
		}
	}

	joinPath := func(t string) string {
		if h1Title != "" {
			return h1Title + " > " + t
		}
		return t
	}

	if len(h2s) == 0 {
		c := strings.TrimSpace(body)
		if c == "" {
			return nil
		}
		return []section{{headingPath: h1Title, content: c}}
	}

	var out []section
	if pre := strings.TrimSpace(body[:h2s[0].offset]); pre != "" {
		out = append(out, section{headingPath: h1Title, content: pre})
	}
	for i, h := range h2s {
		end := len(body)
		if i+1 < len(h2s) {
			end = h2s[i+1].offset
		}
		c := strings.TrimSpace(body[h.offset:end])
		if c != "" {
			out = append(out, section{headingPath: joinPath(h.title), content: c})
		}
	}
	return out
}

// mergeSmall folds undersized sections into their predecessor so we don't
// embed fragments.
func mergeSmall(in []section) []section {
	var out []section
	for _, s := range in {
		if len(out) > 0 && len(s.content) < mergeBelow {
			out[len(out)-1].content += "\n\n" + s.content
			continue
		}
		out = append(out, s)
	}
	// A lone undersized first section can't merge backwards; keep it.
	return out
}

// hardSplit breaks oversized content at paragraph boundaries so no chunk
// exceeds the embedding model's comfortable context.
func hardSplit(content string) []string {
	if len(content) <= hardSplitOver {
		return []string{content}
	}
	paras := strings.Split(content, "\n\n")
	var out []string
	var cur strings.Builder
	for _, p := range paras {
		if cur.Len() > 0 && cur.Len()+len(p)+2 > hardSplitOver {
			out = append(out, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(p)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
