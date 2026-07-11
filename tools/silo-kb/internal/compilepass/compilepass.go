// Package compilepass runs the knowledge lifecycle over knowledge/**:
// reinforce → decay → archive → graduate, then appends an audit entry to
// knowledge/log.md. Reinforcement and graduation are explicit CLI inputs —
// the agent driving a run decides and justifies them; nothing is inferred.
// projects/** is asserted canon and is never touched.
package compilepass

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"silo.local/silo-kb/internal/links"
	"silo.local/silo-kb/internal/validate"
	"silo.local/silo-kb/internal/vault"
)

const (
	reinforceDelta = 0.1
	decayDelta     = 0.1
	staleAfter     = 30 * 24 * time.Hour
	ancientAfter   = 6 * 30 * 24 * time.Hour
	stableMinRuns  = 3
	developingAt   = 0.8
	stableAt       = 0.9
	// refreshWithin: a theory cited by a file committed this recently is still in
	// use, so its decay clock resets without an explicit reinforce (passive
	// citation-as-provenance refresh).
	refreshWithin = 7 * 24 * time.Hour
)

type Options struct {
	Reinforce []string          // note ids or vault-relative paths
	Graduate  map[string]string // id (or path) -> vault-relative destination under projects/
	Falsify   map[string]string // id (or path) -> reason the theory was determined false
	Dispute   map[string]string // id (or path) -> reason the theory is contested (live, not disproven)
	Supersede map[string]string // falsified note id (or path) -> replacement note id (or path)
	DryRun    bool
	Now       time.Time
}

type Action struct {
	Kind   string // reinforced | refreshed | decayed | archived-faded | archived-ancient | falsified | disputed | dispute-cleared | graduated | promoted
	Note   string // wikilink basename
	Detail string
}

type Report struct {
	When    time.Time
	Actions []Action
	// StableCandidates are live stable notes not graduated this run — the
	// shortlist an agent may want to --graduate next time. Report-only.
	StableCandidates []string
}

func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s compile\n", r.When.Format(validate.TimeLayout))
	if len(r.Actions) == 0 {
		b.WriteString("- no changes\n")
	}
	for _, a := range r.Actions {
		fmt.Fprintf(&b, "- %s: [[%s]] %s\n", a.Kind, a.Note, a.Detail)
	}
	if len(r.StableCandidates) > 0 {
		links := make([]string, len(r.StableCandidates))
		for i, c := range r.StableCandidates {
			links[i] = "[[" + c + "]]"
		}
		fmt.Fprintf(&b, "- stable, ungraduated (graduation candidates): %s\n", strings.Join(links, ", "))
	}
	return b.String()
}

// Run executes a compilation pass rooted at repoRoot (the directory containing
// knowledge-base/).
func Run(repoRoot string, opts Options) (*Report, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	vaultRoot := filepath.Join(repoRoot, "knowledge-base")
	notes, err := vault.Walk(vaultRoot)
	if err != nil {
		return nil, err
	}

	reinforce := toSet(opts.Reinforce)
	graduate := map[string]string{}
	for k, v := range opts.Graduate {
		graduate[k] = v
	}
	falsify := map[string]string{}
	for k, v := range opts.Falsify {
		falsify[k] = v
	}
	dispute := map[string]string{}
	for k, v := range opts.Dispute {
		dispute[k] = v
	}
	supersede := map[string]string{}
	for k, v := range opts.Supersede {
		supersede[k] = v
	}

	// id/path -> wikilink basename, so a --supersede target resolves to the
	// wikilink written into the falsified note's `superseded_by`.
	wikiByKey := map[string]string{}
	for _, n := range notes {
		wikiByKey[n.ID()] = wikiname(n.Path)
		wikiByKey[n.Path] = wikiname(n.Path)
	}

	// Every requested id/path must resolve to a live knowledge note — a typo
	// silently doing nothing would let the agent report work that never
	// happened. Checked up front, before any write.
	// A falsified note is retained and queryable but terminal for the
	// lifecycle: it is not a valid target of reinforce/graduate/falsify, so it
	// is excluded from the live set (naming one is a typo worth surfacing).
	live := map[string]bool{}
	for _, n := range notes {
		if n.Tier == vault.TierKnowledge && !strings.HasPrefix(n.Path, "knowledge/archive/") && statusOf(n) != validate.StatusFalsified {
			live[n.ID()] = true
			live[n.Path] = true
		}
	}
	var unknown []string
	for k := range reinforce {
		if !live[k] {
			unknown = append(unknown, "--reinforce "+k)
		}
	}
	for k := range graduate {
		if !live[k] {
			unknown = append(unknown, "--graduate "+k)
		}
	}
	for k := range falsify {
		if !live[k] {
			unknown = append(unknown, "--falsify "+k)
		}
	}
	for k := range dispute {
		if !live[k] {
			unknown = append(unknown, "--dispute "+k)
		}
	}
	// Every --supersede must attach to a note being falsified this run, and its
	// replacement must resolve to a real note.
	for k, repl := range supersede {
		if _, ok := falsify[k]; !ok {
			unknown = append(unknown, "--supersede "+k+" (no matching --falsify)")
		}
		if _, ok := wikiByKey[repl]; !ok {
			unknown = append(unknown, "--supersede replacement "+repl)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("no live knowledge note matches:\n  %s\n(ids/paths must name a note under knowledge/ outside archive/)",
			strings.Join(unknown, "\n  "))
	}

	// A single run must not both re-confirm and contest/invalidate the same note:
	// today falsify silently wins over reinforce by loop order, hiding the
	// contradiction. Reject it so the invoking agent — which has the context —
	// decides what it actually means, rather than freezing the note behind a lock.
	// Keys may be an id or a path, so normalize to id before comparing.
	idOf := map[string]string{}
	for _, n := range notes {
		idOf[n.ID()] = n.ID()
		idOf[n.Path] = n.ID()
	}
	op := map[string][]string{} // note id -> the mutating ops naming it this run
	for _, m := range []struct {
		name string
		keys map[string]bool
	}{
		{"reinforce", reinforce},
		{"falsify", toKeys(falsify)},
		{"dispute", toKeys(dispute)},
	} {
		for k := range m.keys {
			if id, ok := idOf[k]; ok {
				op[id] = append(op[id], m.name)
			}
		}
	}
	var conflicts []string
	for id, ops := range op {
		if len(ops) > 1 {
			sort.Strings(ops)
			conflicts = append(conflicts, fmt.Sprintf("%s: %s", id, strings.Join(ops, " + ")))
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return nil, fmt.Errorf("contradictory operations on the same note in one run (pick one):\n  %s",
			strings.Join(conflicts, "\n  "))
	}

	// Reverse citation index for passive decay-refresh: a theory still cited by
	// recently-committed work is being used, so its clock resets even without an
	// explicit reinforce. Keyed on git commit time (the same signal ancient-
	// archival trusts) — fs mtime lies after checkouts. Confidence is untouched:
	// being referenced keeps a note alive; only an agent's reinforce asserts it.
	idByName := map[string]string{} // wikilink basename -> note id (first wins)
	for _, n := range notes {
		name := wikiname(n.Path)
		if _, ok := idByName[name]; !ok {
			idByName[name] = n.ID()
		}
	}
	recentlyCited := map[string]bool{} // note id -> cited by a file committed < refreshWithin ago
	for _, m := range notes {
		age, ok := gitAge(repoRoot, filepath.Join("knowledge-base", m.Path), opts.Now)
		if !ok || age > refreshWithin {
			continue
		}
		for _, ref := range links.Targets(m) {
			if id, ok := idByName[ref.Name]; ok && id != m.ID() {
				recentlyCited[id] = true
			}
		}
	}

	report := &Report{When: opts.Now}

	for _, n := range notes {
		if n.Tier != vault.TierKnowledge || strings.HasPrefix(n.Path, "knowledge/archive/") {
			continue
		}
		name := wikiname(n.Path)
		abs := filepath.Join(vaultRoot, n.Path)

		// An already-falsified note is retained but inert: it stays live and
		// queryable for as-of/history, but never decays, archives, or graduates.
		// Skip it before any lifecycle branch (it was excluded from the live set,
		// so it is never a reinforce/graduate/falsify target either).
		if statusOf(n) == validate.StatusFalsified {
			continue
		}

		// 0. Falsify (explicit, wins over everything). A note the agent has
		// determined false is invalidated in place with its reason — it was
		// wrong, not forgotten. It stays in knowledge/ (queryable, but frozen and
		// excluded from default retrieval), bypassing the confidence/decay
		// machinery. `timestamp`..`falsified_at` is the window it was believed.
		if reason, ok := pick(falsify, n.ID(), n.Path); ok {
			if strings.TrimSpace(reason) == "" {
				return nil, fmt.Errorf("%s: refusing to falsify without a reason", n.Path)
			}
			detail := "(" + reason + ")"
			if repl, ok := pick(supersede, n.ID(), n.Path); ok {
				detail += " → [[" + wikiByKey[repl] + "]]"
			}
			report.Actions = append(report.Actions, Action{"falsified", name, detail})
			if !opts.DryRun {
				setFM(n.FMNode, "status", validate.StatusFalsified)
				setFM(n.FMNode, "falsified_reason", reason)
				setFM(n.FMNode, "falsified_at", opts.Now.Format(validate.TimeLayout))
				if repl, ok := pick(supersede, n.ID(), n.Path); ok {
					setFM(n.FMNode, "superseded_by", "[["+wikiByKey[repl]+"]]")
				}
				// Retained in place: validate against the (unchanged) live path and
				// write — no move.
				if err := writeNote(abs, n); err != nil {
					return nil, err
				}
			}
			continue
		}

		// 0.5. Dispute (explicit, non-terminal). Unlike falsify, a disputed note is
		// contested but not disproven: it stays live and keeps decaying. This is the
		// automatic middle ground — no human lock, no frozen fields. A later
		// reinforce (an agent re-asserting) clears it back to active.
		if reason, ok := pick(dispute, n.ID(), n.Path); ok {
			if strings.TrimSpace(reason) == "" {
				return nil, fmt.Errorf("%s: refusing to dispute without a reason", n.Path)
			}
			report.Actions = append(report.Actions, Action{"disputed", name, "(" + reason + ")"})
			if !opts.DryRun {
				setFM(n.FMNode, "status", "disputed")
				setFM(n.FMNode, "disputed_reason", reason)
				setFM(n.FMNode, "disputed_at", opts.Now.Format(validate.TimeLayout))
				if err := writeNote(abs, n); err != nil {
					return nil, err
				}
			}
			continue
		}

		conf := toFloat(n.Frontmatter["confidence"])
		count := toInt(n.Frontmatter["reinforce_count"])
		maturity, _ := n.Frontmatter["maturity"].(string)
		last, err := validate.TimeOf(n.Frontmatter["last_reinforced"])
		if err != nil {
			return nil, fmt.Errorf("%s: last_reinforced: %v", n.Path, err)
		}
		lastStr := last.Format(validate.TimeLayout)

		reinforced := reinforce[n.ID()] || reinforce[n.Path]
		// A paused note (blocked on something external) or one still cited by
		// recently-committed work is shielded from decay: its clock resets to now
		// instead of ticking down. Confidence is never *raised* this way — being
		// referenced keeps a note alive; only an agent's reinforce asserts it.
		paused := statusOf(n) == "paused"
		shielded := paused || recentlyCited[n.ID()]
		clearDispute := false
		changed := false

		// 1. Reinforce (wins over decay in the same run).
		if reinforced {
			old := conf
			conf = min(1.0, conf+reinforceDelta)
			count++
			last = opts.Now
			changed = true
			report.Actions = append(report.Actions, Action{"reinforced", name,
				fmt.Sprintf("%.1f→%.1f", old, conf)})
			// An agent re-asserting a contested note resolves the dispute.
			if statusOf(n) == "disputed" {
				clearDispute = true
				report.Actions = append(report.Actions, Action{"dispute-cleared", name, "(reinforced)"})
			}
			// Maturity promotions only advance on reinforcement.
			if maturity == "seed" && conf >= developingAt {
				maturity = "developing"
				report.Actions = append(report.Actions, Action{"promoted", name, "seed→developing"})
			} else if maturity == "developing" && conf >= stableAt && count >= stableMinRuns {
				maturity = "stable"
				report.Actions = append(report.Actions, Action{"promoted", name, "developing→stable"})
			}
		} else if opts.Now.Sub(last) > staleAfter {
			if shielded {
				// 2a. Refresh: reset the decay clock without touching confidence.
				cause := "cited recently"
				if paused {
					cause = "paused"
				}
				last = opts.Now
				changed = true
				report.Actions = append(report.Actions, Action{"refreshed", name, "(" + cause + ")"})
			} else {
				// 2b. Decay.
				old := conf
				conf = max(0.0, conf-decayDelta)
				changed = true
				report.Actions = append(report.Actions, Action{"decayed", name,
					fmt.Sprintf("%.1f→%.1f (last_reinforced %s)", old, conf, lastStr)})
			}
		}

		if changed && !opts.DryRun {
			setFM(n.FMNode, "confidence", fmt.Sprintf("%.1f", conf))
			setFM(n.FMNode, "maturity", maturity)
			setFM(n.FMNode, "last_reinforced", last.Format(validate.TimeLayout))
			setFM(n.FMNode, "reinforce_count", strconv.Itoa(count))
			if clearDispute {
				setFM(n.FMNode, "status", "active")
				deleteFM(n.FMNode, "disputed_reason")
				deleteFM(n.FMNode, "disputed_at")
			}
			if err := writeNote(abs, n); err != nil {
				return nil, err
			}
		}

		// 3. Archive. Faded check first; both are moves, id survives.
		if conf <= 0 {
			dest := filepath.Join("knowledge/archive/faded", filepath.Base(n.Path))
			report.Actions = append(report.Actions, Action{"archived-faded", name,
				fmt.Sprintf("(confidence %.1f)", conf)})
			if !opts.DryRun {
				if err := moveNote(vaultRoot, n.Path, dest); err != nil {
					return nil, err
				}
			}
			continue
		}
		// Reinforcement this run shields a note from ancient-archival: git age
		// lags until the rewrite is committed, and a note the agent just
		// re-confirmed is by definition not abandoned. A paused or recently-cited
		// note is likewise still in play, not abandoned.
		if age, ok := gitAge(repoRoot, filepath.Join("knowledge-base", n.Path), opts.Now); !reinforced && !shielded && ok && age > ancientAfter {
			dest := filepath.Join("knowledge/archive", filepath.Base(n.Path))
			report.Actions = append(report.Actions, Action{"archived-ancient", name,
				fmt.Sprintf("(last commit %s ago)", age.Round(24*time.Hour))})
			if !opts.DryRun {
				if err := moveNote(vaultRoot, n.Path, dest); err != nil {
					return nil, err
				}
			}
			continue
		}

		// 4. Graduate (explicit).
		if dest, ok := pick(graduate, n.ID(), n.Path); ok {
			if maturity != "stable" {
				return nil, fmt.Errorf("%s: refusing to graduate non-stable note (maturity %s)", n.Path, maturity)
			}
			if paused {
				return nil, fmt.Errorf("%s: refusing to graduate a paused note; unpause it first (status: active)", n.Path)
			}
			// Contested theory must not become canon. Resolve the dispute first — a
			// reinforce clears it automatically — so graduation reflects settled
			// belief, not an open argument.
			if statusOf(n) == "disputed" {
				return nil, fmt.Errorf("%s: refusing to graduate a disputed note; resolve the dispute first (reinforce clears it)", n.Path)
			}
			// Depth matters: the indexer only sees projects/<name>/<note>.md —
			// a note directly under projects/ would silently vanish from search.
			if parts := strings.Split(dest, "/"); len(parts) < 3 || parts[0] != "projects" {
				return nil, fmt.Errorf("%s: graduation destination must be projects/<name>/<note>.md, got %s", n.Path, dest)
			}
			report.Actions = append(report.Actions, Action{"graduated", name, "→ " + dest})
			if !opts.DryRun {
				for _, f := range []string{"confidence", "maturity", "last_reinforced", "reinforce_count"} {
					deleteFM(n.FMNode, f)
				}
				// Validate against the destination: the note is canon now.
				src := n.Path
				n.Path = dest
				if err := writeNote(abs, n); err != nil {
					return nil, err
				}
				if err := moveNote(vaultRoot, src, dest); err != nil {
					return nil, err
				}
			}
			continue
		}

		// Still live and stable: surface as a graduation candidate for the
		// agent driving the next run. A paused note is on hold, not a candidate.
		if maturity == "stable" && !paused {
			report.StableCandidates = append(report.StableCandidates, name)
		}
	}

	if !opts.DryRun {
		if err := appendLog(vaultRoot, report); err != nil {
			return nil, err
		}
	}
	return report, nil
}

func appendLog(vaultRoot string, r *Report) error {
	logPath := filepath.Join(vaultRoot, "knowledge/log.md")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n" + r.String())
	return err
}

// writeNote re-serializes frontmatter + body, validating the result before it
// touches disk.
func writeNote(abs string, n *vault.Note) error {
	fmBytes, err := yaml.Marshal(n.FMNode)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := yaml.Unmarshal(fmBytes, &m); err != nil {
		return err
	}
	if errs := validate.Note(n.Path, m, true); len(errs) > 0 {
		return fmt.Errorf("%s: refusing to write invalid frontmatter: %s", n.Path, strings.Join(errs, "; "))
	}
	content := "---\n" + string(fmBytes) + "---\n" + n.Body
	return os.WriteFile(abs, []byte(content), 0o644)
}

func moveNote(vaultRoot, from, to string) error {
	absTo := filepath.Join(vaultRoot, to)
	if err := os.MkdirAll(filepath.Dir(absTo), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(absTo); err == nil {
		return fmt.Errorf("move %s: destination %s already exists", from, to)
	}
	return os.Rename(filepath.Join(vaultRoot, from), absTo)
}

// gitAge returns time since the file's last commit. ok is false for untracked
// files or outside a repo — fs mtime is a lie after checkouts, so we never
// fall back to it.
func gitAge(repoRoot, relPath string, now time.Time) (time.Duration, bool) {
	out, err := exec.Command("git", "-C", repoRoot, "log", "-1", "--format=%ct", "--", relPath).Output()
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, false
	}
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return now.Sub(time.Unix(sec, 0)), true
}

// --- yaml.Node surgery (preserves key order and comments) ---

func mapping(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func setFM(doc *yaml.Node, key, value string) {
	m := mapping(doc)
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].SetString(value)
			// SetString forces !!str; clear the tag so numbers stay numbers.
			m.Content[i+1].Tag = ""
			m.Content[i+1].Style = 0
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

func deleteFM(doc *yaml.Node, key string) {
	m := mapping(doc)
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// --- small helpers ---

func toSet(xs []string) map[string]bool {
	s := map[string]bool{}
	for _, x := range xs {
		if x != "" {
			s[x] = true
		}
	}
	return s
}

func toKeys(m map[string]string) map[string]bool {
	s := map[string]bool{}
	for k := range m {
		s[k] = true
	}
	return s
}

func pick(m map[string]string, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return "", false
}

func wikiname(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

func statusOf(n *vault.Note) string {
	s, _ := n.Frontmatter["status"].(string)
	return s
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}
