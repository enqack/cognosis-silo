package validate

import (
	"strings"
	"testing"
)

func TestReservedFiles(t *testing.T) {
	if errs := Note("index.md", map[string]any{"okf_version": "0.1"}, true); len(errs) != 0 {
		t.Errorf("root index.md with okf_version should pass: %v", errs)
	}
	if errs := Note("index.md", map[string]any{"okf_version": "0.1", "extra": 1}, true); len(errs) == 0 {
		t.Error("root index.md with extra fields should fail")
	}
	if errs := Note("knowledge/index.md", nil, false); len(errs) != 0 {
		t.Errorf("non-root index.md without frontmatter should pass: %v", errs)
	}
	if errs := Note("knowledge/index.md", map[string]any{"type": "x"}, true); len(errs) == 0 {
		t.Error("non-root index.md with frontmatter should fail")
	}
	if errs := Note("knowledge/log.md", nil, false); len(errs) != 0 {
		t.Errorf("log.md without frontmatter should pass: %v", errs)
	}
}

func validKnowledgeFM() map[string]any {
	return map[string]any{
		"id":              "de6d5441-c97e-415f-b5ca-0df850ff0d84",
		"type":            "concept",
		"confidence":      0.7,
		"maturity":        "seed",
		"last_reinforced": "2026-07-07 09:00:00",
		"reinforce_count": 0,
		"sources":         []any{"[[2026-07-07]]"},
	}
}

func TestKnowledgeContract(t *testing.T) {
	if errs := Note("knowledge/concepts/x.md", validKnowledgeFM(), true); len(errs) != 0 {
		t.Fatalf("valid knowledge note should pass: %v", errs)
	}

	for _, field := range []string{"id", "confidence", "maturity", "last_reinforced", "reinforce_count", "sources"} {
		fm := validKnowledgeFM()
		delete(fm, field)
		if errs := Note("knowledge/concepts/x.md", fm, true); len(errs) == 0 {
			t.Errorf("missing %s should fail", field)
		}
	}

	fm := validKnowledgeFM()
	fm["confidence"] = 1.5
	if errs := Note("knowledge/concepts/x.md", fm, true); len(errs) == 0 {
		t.Error("confidence > 1 should fail")
	}

	fm = validKnowledgeFM()
	fm["last_reinforced"] = "2026-07-07"
	if errs := Note("knowledge/concepts/x.md", fm, true); len(errs) != 0 {
		t.Errorf("bare date should be accepted: %v", errs)
	}

	fm = validKnowledgeFM()
	fm["id"] = "not-a-uuid"
	if errs := Note("knowledge/concepts/x.md", fm, true); len(errs) == 0 {
		t.Error("invalid uuid should fail")
	}
}

func TestKnowledgeStatus(t *testing.T) {
	for _, s := range []string{"active", "disputed", "paused"} {
		fm := validKnowledgeFM()
		fm["status"] = s
		if errs := Note("knowledge/concepts/x.md", fm, true); len(errs) != 0 {
			t.Errorf("status %q should pass: %v", s, errs)
		}
	}
	fm := validKnowledgeFM()
	fm["status"] = "falsified" // falsified is a move into archive/, not a live status
	if errs := Note("knowledge/concepts/x.md", fm, true); len(errs) == 0 {
		t.Error("status=falsified on a live note should fail")
	}
	fm = validKnowledgeFM()
	fm["status"] = "bogus"
	if errs := Note("knowledge/concepts/x.md", fm, true); len(errs) == 0 {
		t.Error("unknown status should fail")
	}
}

func TestDeepThoughtRequiresDescription(t *testing.T) {
	base := map[string]any{
		"id":   "de6d5441-c97e-415f-b5ca-0df850ff0d84",
		"type": "deep-thought",
	}
	if errs := Note("deep-thoughts/2026-07-07-x.md", base, true); len(errs) == 0 {
		t.Error("deep-thought without description should fail (its body is not embedded)")
	}
	base["description"] = "Refactored the compile lifecycle to add passive citation refresh."
	if errs := Note("deep-thoughts/2026-07-07-x.md", base, true); len(errs) != 0 {
		t.Errorf("deep-thought with a factual description should pass: %v", errs)
	}
}

func TestArchivedNotesExemptFromDecayFields(t *testing.T) {
	fm := map[string]any{"id": "de6d5441-c97e-415f-b5ca-0df850ff0d84", "type": "concept"}
	if errs := Note("knowledge/archive/faded/x.md", fm, true); len(errs) != 0 {
		t.Errorf("archived note without decay fields should pass: %v", errs)
	}
}

func TestProjectsRejectDecayFields(t *testing.T) {
	fm := map[string]any{
		"id":         "f14690fc-adf2-4f6c-baa4-a6c5515fc0a4",
		"type":       "overview",
		"confidence": 0.9,
	}
	errs := Note("projects/silo-kb/overview.md", fm, true)
	if len(errs) == 0 {
		t.Fatal("projects note with confidence should fail")
	}
	if !strings.Contains(errs[0], "asserted canon") {
		t.Errorf("error should explain the canon rule: %v", errs[0])
	}
}

func TestNoFrontmatter(t *testing.T) {
	if errs := Note("daily/2026-07-07.md", nil, false); len(errs) == 0 {
		t.Error("note without frontmatter should fail")
	}
}
