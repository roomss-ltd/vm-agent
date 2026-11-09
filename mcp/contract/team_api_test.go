package main

// Tests for GET /agent/feature/team (who is on a feature's team, how fresh the terminal sample is, which steps each
// agent owns) and for the expected-thread guard on POST /agent/feature/doc and /log. The terminal sample is injected
// straight into the panel's tracker (sampleAt): freshness is term.lastAt, set together with term.last under term.mu.
// Team members are keyed session:pane, and tabs that share a name are flagged ambiguousTab.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sampleAt installs tm as the listing the panel's terminal sampler last completed, at time at. A zero at means the
// panel has never sampled.
func sampleAt(v *supEnv, tm Terminals, at time.Time) {
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list = tm, tm
	v.e.s.term.lastAt = at
	v.e.s.term.mu.Unlock()
}

// teamResp is the JSON answer of GET /agent/feature/team.
type teamResp struct {
	Session    string `json:"session"`
	Thread     string `json:"thread"`
	Present    bool   `json:"present"`
	ObservedAt int64  `json:"observedAt"`
	AgeSec     int64  `json:"ageSec"`
	Stale      bool   `json:"stale"`
	DossierRev string `json:"dossierRev"`
	Team       []struct {
		Key, Tab, Kind, State, Title string
		Pane                         int      `json:"pane"`
		AmbiguousTab                 bool     `json:"ambiguousTab"`
		LastAt                       int64    `json:"lastAt"`
		Owns                         []string `json:"owns"`
	} `json:"team"`
}

// get issues an authenticated GET against the agent API and returns the status code and the body.
func (w *writerEnv) get(t *testing.T, path string) (int, []byte) {
	req, _ := http.NewRequest("GET", w.url+path, nil)
	req.Header.Set("Authorization", "Bearer contract-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 1<<16)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, []byte(b.String())
}

// team fetches /agent/feature/team for supTeam and decodes it, failing the test on anything but a 200.
func (w *writerEnv) team(t *testing.T) teamResp {
	c, b := w.get(t, "/agent/feature/team?session="+supTeam)
	var r teamResp
	if c != 200 || json.Unmarshal(b, &r) != nil {
		t.Fatalf("team: %d %s", c, b)
	}
	return r
}

// pane builds a Claude pane in the given state whose agent was last active at lastAt (unix seconds).
func pane(id int, state string, lastAt int64) TPane {
	return TPane{ID: id, Kind: "claude", State: state, Agent: &TAgent{PID: 40 + id, LastAt: lastAt}}
}

// TestVMAgentTeamAPI checks the team read: freshness (never sampled, fresh, stale) comes from the sample's own time;
// an unknown session is a 404 and an unsampled one is not present; steps are owned by the exact tab name, never a
// prefix, and duplicate tab names are flagged rather than guessed; pane state and activity time pass through unchanged;
// and the expected-thread guard refuses writes from a previous owner after a rebind. vm_mcp's rendering is checked too.
func TestVMAgentTeamAPI(t *testing.T) {
	plan := "## Ship\n- [ ] Build it @Builder\n- [ ] Prefix trap @Build\n- [ ] Case @builder\n"
	tabs := func(ps ...TTab) Terminals { return Terminals{Sessions: []TSession{{Name: supTeam, Tabs: ps}}} }

	t.Run("T1 freshness: never, fresh, stale; observedAt is the sample's time, not the call's", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		tm := tabs(TTab{ID: 1, Name: "Builder", Panes: []TPane{pane(3, "working", time.Now().Add(-time.Hour).Unix())}})
		sampleAt(w.v, tm, time.Time{})
		if r := w.team(t); r.ObservedAt != 0 || r.AgeSec != -1 || !r.Stale {
			bad = append(bad, fmt.Sprintf("never sampled: %+v", r))
		}
		if out := w.py("m.get_feature_team(sys.argv[1])", supTeam); !strings.Contains(out, "never sampled") {
			bad = append(bad, "vm_mcp never: "+out)
		}
		at := time.Now().Add(-10 * time.Second)
		sampleAt(w.v, tm, at)
		if r := w.team(t); r.ObservedAt != at.Unix() || r.Stale || r.AgeSec < 9 || r.AgeSec > 30 {
			bad = append(bad, fmt.Sprintf("fresh: %+v", r))
		}
		old := time.Now().Add(-5 * time.Minute)
		sampleAt(w.v, tm, old)
		r := w.team(t)
		if r.ObservedAt != old.Unix() || !r.Stale {
			bad = append(bad, fmt.Sprintf("stale: %+v", r))
		}
		if r.ObservedAt == r.Team[0].LastAt {
			bad = append(bad, "observedAt equals the agent's activity time")
		}
		if out := w.py("m.get_feature_team(sys.argv[1])", supTeam); !strings.Contains(out, "STALE") {
			bad = append(bad, "vm_mcp stale: "+out)
		}
		scenario(t, "T1 team freshness (never/fresh/stale) from the sample's own time", &bad)
	})

	t.Run("T2 missing session, absent from sample, missing member", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		if c, _ := w.get(t, "/agent/feature/team?session=no-such-feature"); c != 404 {
			bad = append(bad, fmt.Sprintf("unknown session: %d, not 404", c))
		}
		sampleAt(w.v, Terminals{}, time.Now())
		if r := w.team(t); r.Present || len(r.Team) != 0 {
			bad = append(bad, fmt.Sprintf("record not in the sample: %+v", r))
		}
		sampleAt(w.v, tabs(TTab{ID: 1, Name: "Builder", Panes: []TPane{pane(3, "working", 0)}}), time.Now())
		if out := w.py("m.get_team_member(sys.argv[1], 99)", supTeam); !strings.HasPrefix(out, "REFUSED") {
			bad = append(bad, "missing member: "+out)
		}
		scenario(t, "T2 404 unknown, present:false when unsampled, unknown member refused", &bad)
	})

	sup4 := supTeam + ":4"
	t.Run("T3 ownership is the exact tab name (case-insensitive), never a prefix; shared tab names flagged", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		sampleAt(w.v, tabs(TTab{ID: 1, Name: "Builder", Panes: []TPane{pane(3, "working", 0)}}), time.Now())
		d := w.v.e.s.featDossier(supTeam)
		want := []string{d.Milestones[0].Steps[0].Key, d.Milestones[0].Steps[2].Key}
		if got := w.team(t).Team[0].Owns; fmt.Sprint(got) != fmt.Sprint(want) {
			bad = append(bad, fmt.Sprintf("owns %v, want %v (not the @Build step)", got, want))
		}
		sampleAt(w.v, tabs(TTab{ID: 1, Name: "Builder", Panes: []TPane{pane(3, "working", 0)}}, TTab{ID: 2, Name: "builder", Panes: []TPane{pane(4, "waiting", 0)}}), time.Now())
		dup := w.team(t)
		if len(dup.Team) != 2 || !dup.Team[0].AmbiguousTab || !dup.Team[1].AmbiguousTab || fmt.Sprint(dup.Team[0].Owns) != fmt.Sprint(dup.Team[1].Owns) {
			bad = append(bad, fmt.Sprintf("two tabs named builder: both must be ambiguousTab with the same owns: %+v", dup.Team))
		}
		if out := w.py("m.get_team_member(sys.argv[1], 4)", supTeam); !strings.Contains(out, sup4) || strings.Contains(out, supTeam+":3 ·") {
			bad = append(bad, "the pane key must select exactly one member: "+out)
		}
		if out := w.py("m.get_feature_team(sys.argv[1])", supTeam); !strings.Contains(out, "AMBIGUOUS") {
			bad = append(bad, "two tabs named builder not flagged: "+out)
		}
		scenario(t, "T3 exact @Tab ownership; duplicate tab names flagged, not guessed", &bad)
	})

	t.Run("T4 healthy busy vs inactive: state and activity time pass through unchanged", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		hour := time.Now().Add(-time.Hour).Unix()
		sampleAt(w.v, tabs(TTab{ID: 1, Name: "Builder", Panes: []TPane{pane(3, "working", hour)}}, TTab{ID: 2, Name: "Tester", Panes: []TPane{pane(4, "waiting", hour)}}), time.Now())
		r := w.team(t)
		st := map[string]string{}
		for _, a := range r.Team {
			st[a.Tab] = a.State
			if a.LastAt != hour {
				bad = append(bad, fmt.Sprintf("%s lastAt %d, want the activity time %d", a.Tab, a.LastAt, hour))
			}
		}
		if st["Builder"] != "working" || st["Tester"] != "waiting" {
			bad = append(bad, fmt.Sprintf("states %v", st))
		}
		scenario(t, "T4 a long tool call (working, old activity) is distinguishable from idle (waiting)", &bad)
	})

	t.Run("T5 thread guard: mismatch, standalone, absent, rebind interleave", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		thread := w.v.e.s.featDossier(supTeam).Thread
		post := func(path string, body map[string]string) (int, map[string]any) {
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
		read := func(n string) string {
			b, _ := os.ReadFile(filepath.Join(filepath.Dir(w.v.planFile()), n))
			return string(b)
		}
		rev := w.rev(t, "plan")
		if c, out := post("/agent/feature/doc", map[string]string{"session": supTeam, "doc": "plan", "text": "x\n", "rev": rev, "thread": "other"}); c != 409 || out["thread"] != thread {
			bad = append(bad, fmt.Sprintf("mismatched doc: %d %v", c, out))
		}
		logBefore := read("LOG.md")
		if c, out := post("/agent/feature/log", map[string]string{"session": supTeam, "line": "x", "thread": "other"}); c != 409 || out["thread"] != thread {
			bad = append(bad, fmt.Sprintf("mismatched log: %d %v", c, out))
		}
		if read("PLAN.md") != plan || read("LOG.md") != logBefore {
			bad = append(bad, "a refused write changed the record")
		}
		if c, _ := post("/agent/feature/doc", map[string]string{"session": supTeam, "doc": "plan", "text": plan + "- [ ] w\n", "rev": rev}); c != 200 {
			bad = append(bad, fmt.Sprintf("absent thread (worker): %d", c))
		}
		// rebind interleave: the manager read under its thread; the session goes to another thread; its write is refused
		rev = w.rev(t, "plan")
		w.v.a.mu.Lock()
		w.v.a.thread(supTeam).Team = "elsewhere"
		other := w.v.a.newThread("Other", "")
		other.Team, other.Status = supTeam, "working"
		w.v.a.saveThreads()
		w.v.a.mu.Unlock()
		before := read("PLAN.md")
		if out := w.py("m.update_feature_plan(sys.argv[1], sys.argv[2], 'y\\n', sys.argv[3])", supTeam, thread, rev); !strings.HasPrefix(out, "REFUSED") {
			bad = append(bad, "write by the previous owner after a rebind: "+out)
		}
		if read("PLAN.md") != before {
			bad = append(bad, "the previous owner's write landed")
		}
		// standalone: no owner; a supplied thread is a mismatch (thread "")
		w.v.a.mu.Lock()
		other.Team = ""
		w.v.a.saveThreads()
		w.v.a.mu.Unlock()
		if c, out := post("/agent/feature/log", map[string]string{"session": supTeam, "line": "x", "thread": thread}); c != 409 || out["thread"] != "" {
			bad = append(bad, fmt.Sprintf("standalone with thread: %d %v", c, out))
		}
		scenario(t, "T5 expected-thread guard: 409 {thread: actual}, no writes; absent = worker; rebind refuses the old owner", &bad)
	})
}
