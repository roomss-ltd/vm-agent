package main

// Tests for the feature event log (featsup Events/EvSeq) and POST /agent/feature/assign, through vm_mcp and the raw
// routes. There is one canonical log: the agent route, the owner route and featAssignments all read the same entries,
// and nothing is duplicated into the notes store.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evPage is the JSON page returned by the feature events routes.
type evPage struct {
	Events []struct {
		ID, Kind, Step, Tab, Sid, Thread string
		Seq                              int64
		Relation                         *struct{ Kind, With, Why string }
	} `json:"events"`
	More bool `json:"more"`
}

// events reads the agent route /agent/feature/events for the test feature; q is extra query text such as "&after=ev-2".
func (w *writerEnv) events(t *testing.T, q string) (int, evPage) {
	c, b := w.get(t, "/agent/feature/events?session="+supTeam+q)
	var p evPage
	json.Unmarshal(b, &p)
	return c, p
}

// postAgent POSTs a JSON body to an agent route with the contract token and returns the status and decoded JSON reply.
func (w *writerEnv) postAgent(t *testing.T, path string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", w.url+path, strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer contract-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestVMAgentFeatureEvents checks assign validation and its single canonical event, paging of the event log, and that
// events and their sequence survive a restart and a failed save.
func TestVMAgentFeatureEvents(t *testing.T) {
	plan := "## Ship\n- [ ] Export CSV @Builder\n- [ ] Write docs @Docs\n"
	// keys returns the managing thread's id and the keys of the plan's first two steps.
	keys := func(w *writerEnv) (string, string, string) {
		d := w.v.e.s.featDossier(supTeam)
		return d.Thread, d.Milestones[0].Steps[0].Key, d.Milestones[0].Steps[1].Key
	}

	t.Run("E1 assign: validated, thread-guarded, one canonical event read the same everywhere", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		thread, k1, k2 := keys(w)
		notes0 := len(w.v.notes(""))
		out := w.py("m.assign_feature_job(sys.argv[1], sys.argv[2], sys.argv[3], 'Docs', pane=4, sid='s-claimed', relation='handoff', relation_with=sys.argv[4], why='CSV ready')", supTeam, thread, k2, k1)
		if !strings.Contains(out, "(ev-1)") {
			bad = append(bad, "assign_feature_job: "+out)
		}
		for _, x := range []struct {
			body map[string]any
			want int
			why  string
		}{
			{map[string]any{"session": supTeam, "step": "nope", "tab": "Docs", "thread": thread}, 400, "unknown step"},
			{map[string]any{"session": supTeam, "step": k2, "tab": "", "thread": thread}, 400, "no tab"},
			{map[string]any{"session": supTeam, "step": k2, "tab": "Docs", "sid": strings.Repeat("s", 129), "thread": thread}, 400, "sid too long"},
			{map[string]any{"session": supTeam, "step": k2, "tab": "Docs", "thread": thread, "relation": map[string]string{"kind": "adjacent", "with": k1}}, 400, "unknown relation"},
			{map[string]any{"session": supTeam, "step": k2, "tab": "Docs", "thread": thread, "relation": map[string]string{"kind": "pair", "with": k2}}, 400, "relation with itself"},
			{map[string]any{"session": supTeam, "step": k2, "tab": "Docs", "thread": thread, "relation": map[string]string{"kind": "pair", "with": "s-claimed"}}, 400, "relation with a sid"},
			{map[string]any{"session": supTeam, "step": k2, "tab": "Docs", "thread": "someone-else"}, 409, "wrong thread"},
		} {
			if c, _ := w.postAgent(t, "/agent/feature/assign", x.body); c != x.want {
				bad = append(bad, fmt.Sprintf("%s: %d, want %d", x.why, c, x.want))
			}
		}
		c, p := w.events(t, "")
		if c != 200 || len(p.Events) != 1 {
			bad = append(bad, fmt.Sprintf("events after one valid and seven refused assigns: %d %+v", c, p))
		} else if e := p.Events[0]; e.ID != "ev-1" || e.Kind != "assign" || e.Step != k2 || e.Tab != "Docs" || e.Sid != "s-claimed" || e.Thread != thread || e.Relation == nil || e.Relation.With != k1 || e.Relation.Kind != "handoff" {
			bad = append(bad, fmt.Sprintf("the event: %+v", e))
		}
		_, agentRaw := w.get(t, "/agent/feature/events?session="+supTeam)
		owner := w.v.e.do(t, "GET", "/api/term/feature/events?session="+supTeam, "", w.v.e.authed(w.v.e.login(t)))
		var ob strings.Builder
		buf := make([]byte, 1<<16)
		for {
			n, err := owner.Body.Read(buf)
			ob.Write(buf[:n])
			if err != nil {
				break
			}
		}
		owner.Body.Close()
		if strings.TrimSpace(ob.String()) != strings.TrimSpace(string(agentRaw)) {
			bad = append(bad, "the owner and agent routes return different event logs")
		}
		if as := w.v.e.s.featAssignments(supTeam); len(as) != 1 || as[0].ID != "ev-1" || as[0].Rel == nil || as[0].Rel.With != k1 {
			bad = append(bad, fmt.Sprintf("featAssignments differs: %+v", as))
		}
		if n := len(w.v.notes("")); n != notes0 {
			bad = append(bad, fmt.Sprintf("the assign also wrote %d note(s): a second, divergent log", n-notes0))
		}
		if len(w.v.typed()) != 0 {
			bad = append(bad, "assign typed into a pane")
		}
		scenario(t, "E1 assign validated + guarded; one canonical event (agent = owner route = featAssignments; no notes duplicate)", &bad)
	})

	t.Run("E2 paging by id: ascending, limit, more, nothing lost while new events arrive", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		thread, k1, _ := keys(w)
		assign := func() {
			if c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": thread}); c != 200 {
				t.Fatalf("assign: %d %v", c, out)
			}
		}
		for i := 0; i < 5; i++ {
			assign()
		}
		ids := func(p evPage) string {
			var s []string
			for _, e := range p.Events {
				s = append(s, e.ID)
			}
			return strings.Join(s, ",")
		}
		if _, p := w.events(t, "&after=ev-2&limit=2"); ids(p) != "ev-3,ev-4" || !p.More {
			bad = append(bad, fmt.Sprintf("after ev-2 limit 2: %s more=%v", ids(p), p.More))
		}
		assign() // ev-6 arrives mid-read
		if _, p := w.events(t, "&after=ev-4&limit=200"); ids(p) != "ev-5,ev-6" || p.More {
			bad = append(bad, fmt.Sprintf("after ev-4: %s more=%v (the event that arrived mid-read must be there)", ids(p), p.More))
		}
		if c, _ := w.events(t, "&after=bogus"); c != 400 {
			bad = append(bad, fmt.Sprintf("after=bogus: %d", c))
		}
		if c, p := w.events(t, "&limit=999"); c != 200 || len(p.Events) != 6 {
			bad = append(bad, fmt.Sprintf("limit 999 (clamped to 200): %d, %d events", c, len(p.Events)))
		}
		if out := w.py("m.get_feature_events(sys.argv[1], after='ev-5')", supTeam); !strings.Contains(out, "ev-6") || strings.Contains(out, "ev-5 ") || !strings.Contains(out, "next: after=ev-6") {
			bad = append(bad, "vm_mcp get_feature_events: "+out)
		}
		scenario(t, "E2 events page by id, ascending, limit/more; a mid-read arrival isn't lost", &bad)
	})

	t.Run("E3 persistence: restart keeps events and the sequence; a failed save spends no id; seq is per feature", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		thread, k1, _ := keys(w)
		body := map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": thread}
		w.postAgent(t, "/agent/feature/assign", body)
		w.postAgent(t, "/agent/feature/assign", body)
		w.v.e.s.sup = newFeatSup(w.v.e.s.sup.file) // a restart
		w.v.a.mu.Lock()
		w.v.a.mu.Unlock() //nolint:staticcheck // empty critical section: a happens-before edge that publishes the new supervisor
		featPlanMu.Lock()
		featPlanMu.Unlock() //nolint:staticcheck // an empty critical section: the assign handler takes featPlanMu first
		if _, p := w.events(t, ""); len(p.Events) != 2 {
			bad = append(bad, fmt.Sprintf("after a restart: %d events, want 2", len(p.Events)))
		}
		good := w.v.e.s.sup.file
		parent := filepath.Join(t.TempDir(), "not-a-dir")
		writeFile(t, parent, "x")
		featPlanMu.Lock()
		w.v.e.s.sup.mu.Lock()
		w.v.e.s.sup.file = filepath.Join(parent, "featsup.json")
		w.v.e.s.sup.mu.Unlock()
		featPlanMu.Unlock()
		if c, _ := w.postAgent(t, "/agent/feature/assign", body); c != 500 {
			bad = append(bad, fmt.Sprintf("an assign whose save failed: %d, want 500", c))
		}
		featPlanMu.Lock()
		w.v.e.s.sup.mu.Lock()
		w.v.e.s.sup.file = good
		w.v.e.s.sup.mu.Unlock()
		featPlanMu.Unlock()
		if c, out := w.postAgent(t, "/agent/feature/assign", body); c != 200 || out["id"] != "ev-3" {
			bad = append(bad, fmt.Sprintf("after the failed save the next id is %v (%d), want ev-3: an id was spent or lost", out["id"], c))
		}
		if _, p := w.events(t, ""); len(p.Events) != 3 {
			bad = append(bad, fmt.Sprintf("%d events, want 3 (the failed one doesn't exist)", len(p.Events)))
		}
		// another feature (a live session with its own record, no thread): its own sequence
		os.WriteFile(filepath.Join(w.v.e.s.sys.sockDir, "another-feature"), nil, 0o600)
		writeFile(t, filepath.Join(w.v.e.s.cfg.home, "jarvis-files", "features", "another-feature", "PLAN.md"), "## A\n- [ ] Its step @X\n")
		ok := w.v.e.s.featDossier("another-feature").Milestones[0].Steps[0].Key
		if c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": "another-feature", "step": ok, "tab": "X"}); c != 200 || out["id"] != "ev-1" {
			bad = append(bad, fmt.Sprintf("another feature's first event: %v (%d), want its own ev-1", out["id"], c))
		}
		scenario(t, "E3 events/seq persist across a restart; a failed save spends no id; per-feature sequence", &bad)
	})
}
