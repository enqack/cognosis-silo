package main

import (
	"encoding/json"
	"testing"
)

func TestApplyEdit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		edit hookEdit
		want string
	}{
		{"single", "a b a", hookEdit{OldString: "a", NewString: "x"}, "x b a"},
		{"replace all", "a b a", hookEdit{OldString: "a", NewString: "x", ReplaceAll: true}, "x b x"},
		{"empty old is a no-op", "a b a", hookEdit{NewString: "x"}, "a b a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applyEdit(c.in, c.edit); got != c.want {
				t.Errorf("applyEdit(%q, %+v) = %q, want %q", c.in, c.edit, got, c.want)
			}
		})
	}
}

// The PreToolUse matcher includes MultiEdit; its edits[] array must actually
// decode, or the hook silently fails open and the frontmatter gate is a no-op.
func TestHookInputDecodesMultiEdit(t *testing.T) {
	payload := `{
		"tool_name": "MultiEdit",
		"tool_input": {
			"file_path": "/repo/knowledge-base/knowledge/concepts/x.md",
			"edits": [
				{"old_string": "a", "new_string": "b"},
				{"old_string": "c", "new_string": "d", "replace_all": true}
			]
		}
	}`
	var in hookInput
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		t.Fatal(err)
	}
	if len(in.ToolInput.Edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(in.ToolInput.Edits))
	}
	if !in.ToolInput.Edits[1].ReplaceAll {
		t.Error("replace_all not decoded")
	}
	got := "a c c"
	for _, e := range in.ToolInput.Edits {
		got = applyEdit(got, e)
	}
	if got != "b d d" {
		t.Errorf("sequential application = %q, want %q", got, "b d d")
	}
}
