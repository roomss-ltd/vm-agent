package main

// Tests for draft requirements (FEATURE.draft.md). Agents write the draft through POST /agent/feature/doc with
// doc=draft (rev and thread required) or through vm_mcp's create/update_draft_requirements; only the owner can promote
// ({session, draftRev, featureRev}) or discard ({session, draftRev}) it, on the page. The tests use HTTP only, so this
// file compiles against any panel; on one without drafts it fails at run time.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// draftEnv is a writerEnv for a feature whose outcomes the owner has accepted, plus the folder of its record files.
type draftEnv struct {
	*writerEnv
	dir string
}

// newDraftEnv builds a feature with a verified step, a one-outcome FEATURE.md and the owner's acceptance of it.
func newDraftEnv(t *testing.T) *draftEnv {
	w := newWriterEnv(t, "## Ship\n- [x] Check output\n  - covers: D1, D2\n")
	d := &draftEnv{w, filepath.Dir(w.v.planFile())}
	writeFile(t, d.path("FEATURE.md"), "# Desired outcome\n- [ ] D1: Healthy\n")
	if c := w.ownerVerify(t, 0); c != 200 {
		t.Fatalf("precondition: owner verify %d", c)
	}
	if why := vmAcceptOutcomes(t, w.v, supTeam); why != "" {
		t.Fatal("precondition: " + why)
	}
	return d
}

// path returns the path of a file in the feature's record folder.
func (d *draftEnv) path(n string) string { return filepath.Join(d.dir, n) }

// read returns a record file's contents, or "" if it can't be read.
func (d *draftEnv) read(n string) string { b, _ := os.ReadFile(d.path(n)); return string(b) }

// revs returns the revisions the agent route reports (the draft's included), as a client reads them.
func (d *draftEnv) revs(t *testing.T) map[string]string {
	c, b := d.get(t, "/agent/feature?session="+supTeam)
	var r struct {
		Dossier struct {
			Revs  map[string]string `json:"revs"`
			Draft string            `json:"draft"`
		} `json:"dossier"`
	}
	if c != 200 || json.Unmarshal(b, &r) != nil {
		t.Fatalf("GET /agent/feature: %d %s", c, b)
	}
	return r.Dossier.Revs
}

// owner posts to one of the owner's /api/term/feature/ routes with a logged-in session and returns the HTTP status.
func (d *draftEnv) owner(t *testing.T, route string, body map[string]string) int {
	b, _ := json.Marshal(body)
	return d.v.e.do(t, "POST", "/api/term/feature/"+route, string(b), d.v.e.authed(d.v.e.login(t))).StatusCode
}

// state describes the three things a promotion changes together: FEATURE.md, the draft and the acceptance.
func (d *draftEnv) state() string {
	return fmt.Sprintf("feature=%q draft=%q accepted=%v", d.read("FEATURE.md"), d.read("FEATURE.draft.md"), d.v.e.s.featDossier(supTeam).Accepted)
}

// draft writes text as the feature's draft through vm_mcp, as the managing thread would.
func (d *draftEnv) draft(t *testing.T, text string) {
	thread := d.v.e.s.featDossier(supTeam).Thread
	if out := d.py("m.create_draft_requirements(sys.argv[1], sys.argv[2], sys.argv[3])", supTeam, thread, text); !strings.HasPrefix(out, "OK DRAFT written") {
		t.Fatalf("precondition: draft through vm_mcp: %s", out)
	}
}

// TestVMAgentDraftRequirements checks the draft workflow: who may write, promote or discard a draft, that stale
// revisions change nothing, and that promotion or discard (including a failed promotion) touch only what they should.
func TestVMAgentDraftRequirements(t *testing.T) {
	const next = "# Desired outcome\n- [ ] D1: Healthy\n- [ ] D2: Rollback tested\n"

	t.Run("P1 the agent bearer can't promote or discard", func(t *testing.T) {
		var bad []string
		d := newDraftEnv(t)
		d.draft(t, next)
		agent := httptest.NewServer(d.v.e.s.agentHandler("agent-token"))
		defer agent.Close()
		rv := d.revs(t)
		body := `{"session":"demo-feature","draftRev":"` + rv["draft"] + `","featureRev":"` + rv["feature"] + `"}`
		before := d.state()
		for _, r := range []string{"promote", "discard"} {
			for _, u := range []string{agent.URL + "/api/term/feature/" + r, agent.URL + "/agent/feature/" + r, d.v.e.srv.URL + "/api/term/feature/" + r} {
				req, _ := http.NewRequest("POST", u, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer agent-token")
				if resp, err := http.DefaultClient.Do(req); err == nil {
					resp.Body.Close()
					if resp.StatusCode < 400 {
						bad = append(bad, fmt.Sprintf("%s answered %d to the agent bearer", u, resp.StatusCode))
					}
				}
			}
		}
		if d.state() != before {
			bad = append(bad, "the record changed: "+d.state())
		}
		scenario(t, "P1 promote/discard refuse the agent bearer on both servers", &bad)
	})

	t.Run("P2 draft edits leave the approved FEATURE.md, its outcomes and acceptance alone; same rev and thread boundary", func(t *testing.T) {
		var bad []string
		d := newDraftEnv(t)
		approved, rv := d.read("FEATURE.md"), d.revs(t)
		d.draft(t, next)
		if d.read("FEATURE.draft.md") != next {
			bad = append(bad, fmt.Sprintf("FEATURE.draft.md: %q", d.read("FEATURE.draft.md")))
		}
		thread := d.v.e.s.featDossier(supTeam).Thread
		dr := d.revs(t)["draft"]
		if out := d.py("m.update_draft_requirements(sys.argv[1], sys.argv[2], 'x', 'stale')", supTeam, thread); !strings.Contains(out, "409") {
			bad = append(bad, "stale draft rev: "+out)
		}
		if out := d.py("m.update_draft_requirements(sys.argv[1], 'someone-else', 'x', sys.argv[2])", supTeam, dr); !strings.HasPrefix(out, "REFUSED") {
			bad = append(bad, "another thread's draft write: "+out)
		}
		post := func(body map[string]string) int {
			b, _ := json.Marshal(body)
			req, _ := http.NewRequest("POST", d.url+"/agent/feature/doc", strings.NewReader(string(b)))
			req.Header.Set("Authorization", "Bearer contract-token")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		if c := post(map[string]string{"session": supTeam, "doc": "draft", "text": "x", "rev": dr, "thread": "someone-else"}); c != 409 {
			bad = append(bad, fmt.Sprintf("the panel's thread guard on a draft: %d, not 409", c))
		}
		if c := post(map[string]string{"session": supTeam, "doc": "feature", "text": next, "rev": rv["feature"]}); c != 403 {
			bad = append(bad, fmt.Sprintf("FEATURE.md through the agent route: %d, not 403", c))
		}
		if d.read("FEATURE.draft.md") != next {
			bad = append(bad, "a refused write changed the draft")
		}
		if d.read("FEATURE.md") != approved || d.revs(t)["feature"] != rv["feature"] || !d.v.e.s.featDossier(supTeam).Accepted {
			bad = append(bad, "a draft edit touched the approved record: "+d.state())
		}
		if ok, out := d.vmDone("demo-feature"); !ok {
			bad = append(bad, "a pending draft blocked done on the approved outcomes: "+out)
		}
		scenario(t, "P2 drafts change nothing approved; stale/other-thread/FEATURE writes refused", &bad)
	})

	t.Run("P3 a stale or empty promotion writes nothing", func(t *testing.T) {
		var bad []string
		d := newDraftEnv(t)
		if c := d.owner(t, "promote", map[string]string{"session": supTeam, "draftRev": d.revs(t)["draft"], "featureRev": d.revs(t)["feature"]}); c < 400 {
			bad = append(bad, fmt.Sprintf("promoting no draft answered %d", c))
		}
		d.draft(t, next)
		rv, before := d.revs(t), d.state()
		for _, b := range []map[string]string{
			{"session": supTeam, "draftRev": "stale", "featureRev": rv["feature"]},
			{"session": supTeam, "draftRev": rv["draft"], "featureRev": "stale"},
		} {
			if c := d.owner(t, "promote", b); c != 409 {
				bad = append(bad, fmt.Sprintf("promote %v answered %d, not 409", b, c))
			}
			if d.state() != before {
				bad = append(bad, "a stale promotion wrote: "+d.state())
			}
		}
		if c := d.owner(t, "discard", map[string]string{"session": supTeam, "draftRev": "stale"}); c != 409 || d.state() != before {
			bad = append(bad, fmt.Sprintf("a stale discard: %d, %s", c, d.state()))
		}
		scenario(t, "P3 stale draftRev or featureRev, or no draft: refused, nothing written", &bad)
	})

	t.Run("P4 promotion replaces FEATURE.md, removes the draft, clears acceptance, logs, reopens done; approves nothing", func(t *testing.T) {
		var bad []string
		d := newDraftEnv(t)
		if ok, out := d.vmDone("demo-feature"); !ok {
			t.Fatalf("precondition: done %s", out)
		}
		d.draft(t, next)
		was := d.step(0)
		rv := d.revs(t)
		if c := d.owner(t, "promote", map[string]string{"session": supTeam, "draftRev": rv["draft"], "featureRev": rv["feature"]}); c != 200 {
			t.Fatalf("promote answered %d", c)
		}
		if d.read("FEATURE.md") != next {
			bad = append(bad, fmt.Sprintf("FEATURE.md after promote: %q", d.read("FEATURE.md")))
		}
		if _, err := os.Stat(d.path("FEATURE.draft.md")); err == nil {
			bad = append(bad, "the draft is still there")
		}
		if d.v.e.s.featDossier(supTeam).Accepted {
			bad = append(bad, "the old acceptance carried over to the promoted outcomes")
		}
		if !strings.Contains(strings.ToLower(d.read("LOG.md")), "promot") {
			bad = append(bad, "LOG.md doesn't record the promotion")
		}
		if out := d.py("m.thread_list(True)"); !strings.Contains(out, "[working") {
			bad = append(bad, "done not reopened: "+out)
		}
		if st := d.step(0); st.Approval != was.Approval {
			bad = append(bad, fmt.Sprintf("promotion changed an approval: %+v -> %+v", was.Approval, st.Approval))
		}
		if ok, _ := d.vmDone("demo-feature"); ok {
			bad = append(bad, "done accepted on promoted outcomes the owner hasn't accepted")
		}
		scenario(t, "P4 promote: approved = draft, draft gone, acceptance cleared, logged, reopened, nothing approved", &bad)
	})

	t.Run("P5 a failed promotion leaves approved feature, acceptance and draft as they were", func(t *testing.T) {
		var bad []string
		for _, inject := range []struct {
			name string
			do   func(d *draftEnv) func()
		}{
			{"supervisor state (acceptance) can't be saved", func(d *draftEnv) func() {
				parent := filepath.Join(t.TempDir(), "not-a-dir")
				writeFile(t, parent, "x")
				old := d.v.e.s.sup.file
				d.v.e.s.sup.file = filepath.Join(parent, "sup.json")
				return func() { d.v.e.s.sup.file = old }
			}},
			{"the record folder is read-only", func(d *draftEnv) func() {
				os.Chmod(d.dir, 0o500)
				return func() { os.Chmod(d.dir, 0o755) }
			}},
		} {
			d := newDraftEnv(t)
			d.draft(t, next)
			rv, before := d.revs(t), d.state()
			undo := inject.do(d)
			c := d.owner(t, "promote", map[string]string{"session": supTeam, "draftRev": rv["draft"], "featureRev": rv["feature"]})
			after := d.state()
			undo()
			if c == 200 {
				bad = append(bad, inject.name+": promote answered 200")
			}
			if after != before {
				bad = append(bad, inject.name+": split state "+after+" (was "+before+")")
			}
		}
		scenario(t, "P5 failed promotion: approved FEATURE, acceptance and draft unchanged together", &bad)
	})

	t.Run("P6 discard removes only the draft", func(t *testing.T) {
		var bad []string
		d := newDraftEnv(t)
		d.draft(t, next)
		approved := d.read("FEATURE.md")
		if c := d.owner(t, "discard", map[string]string{"session": supTeam, "draftRev": d.revs(t)["draft"]}); c != 200 {
			t.Fatalf("discard answered %d", c)
		}
		if _, err := os.Stat(d.path("FEATURE.draft.md")); err == nil {
			bad = append(bad, "the draft is still there")
		}
		if d.read("FEATURE.md") != approved || !d.v.e.s.featDossier(supTeam).Accepted {
			bad = append(bad, "discard touched the approved record: "+d.state())
		}
		scenario(t, "P6 discard: draft gone, approved FEATURE and acceptance kept", &bad)
	})
}
