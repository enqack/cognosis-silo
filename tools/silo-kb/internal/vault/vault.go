// Package vault walks the knowledge-base tree, splits frontmatter from body,
// classifies notes into tiers, and enforces the frontmatter contract via the
// validate package. The markdown tree is the single source of truth; everything
// downstream (chunking, indexing, compilation) consumes vault.Note.
package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"silo.local/silo-kb/internal/validate"
)

type Tier string

const (
	TierDaily       Tier = "daily"
	TierDeepThought Tier = "deep-thought"
	TierKnowledge   Tier = "knowledge"
	TierProject     Tier = "project"
)

// Note is one indexable markdown file. Path is relative to the vault root
// (e.g. "knowledge/concepts/foo.md").
type Note struct {
	Path        string
	Tier        Tier
	Project     string // first path segment under projects/, else ""
	Frontmatter map[string]any
	FMNode      *yaml.Node // for lossless round-trip rewrites
	Body        string     // content after frontmatter
	ContentHash string     // sha256 of Body (frontmatter changes don't force re-embeds)
}

// ID returns the note's frontmatter id.
func (n *Note) ID() string {
	id, _ := n.Frontmatter["id"].(string)
	return id
}

// Type returns the note's frontmatter type.
func (n *Note) Type() string {
	t, _ := n.Frontmatter["type"].(string)
	return t
}

// SplitFrontmatter separates a leading YAML frontmatter block from the body.
// hasFM is false when the file has no frontmatter block at all.
func SplitFrontmatter(content []byte) (fm string, body string, hasFM bool) {
	s := string(content)
	if !strings.HasPrefix(s, "---\n") {
		return "", s, false
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n---") {
			return rest[:len(rest)-4], "", true
		}
		return "", s, false
	}
	return rest[:end], rest[end+5:], true
}

// TierOf classifies a vault-relative path. ok is false for paths outside the
// four tiers (e.g. the root index.md).
func TierOf(relPath string) (tier Tier, project string, ok bool) {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	switch parts[0] {
	case "daily":
		return TierDaily, "", true
	case "deep-thoughts":
		return TierDeepThought, "", true
	case "knowledge":
		return TierKnowledge, "", true
	case "projects":
		if len(parts) < 3 {
			return TierProject, "", false
		}
		return TierProject, parts[1], true
	}
	return "", "", false
}

// ParseNote parses a single file's content into a Note (without validation).
func ParseNote(relPath string, content []byte) (*Note, error) {
	fm, body, hasFM := SplitFrontmatter(content)
	tier, project, _ := TierOf(relPath)
	n := &Note{
		Path:    relPath,
		Tier:    tier,
		Project: project,
		Body:    body,
	}
	sum := sha256.Sum256([]byte(body))
	n.ContentHash = hex.EncodeToString(sum[:])
	if hasFM {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(fm), &node); err != nil {
			return nil, fmt.Errorf("%s: invalid YAML frontmatter: %w", relPath, err)
		}
		var m map[string]any
		if err := yaml.Unmarshal([]byte(fm), &m); err != nil {
			return nil, fmt.Errorf("%s: invalid YAML frontmatter: %w", relPath, err)
		}
		n.FMNode = &node
		n.Frontmatter = m
	}
	return n, nil
}

// IsReserved reports whether the basename is index.md or log.md.
func IsReserved(relPath string) bool {
	base := filepath.Base(relPath)
	return base == "index.md" || base == "log.md"
}

// Walk parses and validates every indexable note under root (the
// knowledge-base directory). Reserved files (index.md/log.md) are validated
// against their own contract (root index.md carries exactly okf_version, every
// other one no frontmatter) but excluded from the returned notes; non-markdown
// files are skipped. Validation failures across all files are aggregated into
// the returned error so one run reports everything.
func Walk(root string) ([]*Note, error) {
	var notes []*Note
	var problems []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if IsReserved(rel) {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			n, err := ParseNote(rel, content)
			if err != nil {
				problems = append(problems, err.Error())
				return nil
			}
			for _, e := range validate.Note(rel, n.Frontmatter, n.Frontmatter != nil) {
				problems = append(problems, fmt.Sprintf("%s: %s", rel, e))
			}
			return nil
		}
		if _, _, ok := TierOf(rel); !ok {
			return nil // outside the four tiers
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n, err := ParseNote(rel, content)
		if err != nil {
			problems = append(problems, err.Error())
			return nil
		}
		if errs := validate.Note(rel, n.Frontmatter, n.Frontmatter != nil); len(errs) > 0 {
			for _, e := range errs {
				problems = append(problems, fmt.Sprintf("%s: %s", rel, e))
			}
			return nil
		}
		notes = append(notes, n)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Duplicate ids would make two files fight over one index row (and
	// provenance would silently merge) — reject the whole run.
	byID := map[string]string{}
	for _, n := range notes {
		if prev, dup := byID[n.ID()]; dup {
			problems = append(problems, fmt.Sprintf(
				"duplicate id %s in %s and %s — ids are assigned once and never reused; give one of them a fresh UUID",
				n.ID(), prev, n.Path))
		}
		byID[n.ID()] = n.Path
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("vault validation failed:\n  %s", strings.Join(problems, "\n  "))
	}
	return notes, nil
}
