package main

// Tests for the owner's "seen" receipts and "reviewed" marks on a feature's event log, over the real routes:
// POST /api/term/feature/seen {session, ids[]} and POST /api/term/feature/reviewed {session, id, rev}. Events are made
// by the agent's POST /agent/feature/assign and by typed progress reports (vm_mcp report_feature_progress).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// owner POSTs body as JSON to an owner-only panel route with a logged-in owner session and returns the status code and
// the decoded JSON answer.
func (w *writerEnv) owner(t *testing.T, path string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	r := w.v.e.do(t, "POST", path, string(b), w.v.e.authed(w.v.e.login(t)))
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return r.StatusCode, out
}

// seen sends ids to the owner's seen route for session and returns the status and the answer (frontier, unseen, ...).
// With no ids it records nothing and just reports the current state.
func (w *writerEnv) seen(t *testing.T, session string, ids ...string) (int, map[string]any) {
	if ids == nil {
		ids = []string{}
	}
	return w.owner(t, "/api/term/feature/seen", map[string]any{"session": session, "ids": ids})
}

// restart simulates a panel restart: it swaps in a supervisor reloaded from disk. The empty lock/unlock pairs publish
// the new supervisor through the locks the handlers take.
func (w *writerEnv) restart() {
	w.v.e.s.sup = newFeatSup(w.v.e.s.sup.file)
	w.v.a.mu.Lock()
	w.v.a.mu.Unlock() //nolint:staticcheck
	featPlanMu.Lock()
	featPlanMu.Unlock() //nolint:staticcheck
}

// evRevOf returns the revision the panel gave that event, read through the events route (empty if there is none).
func (w *writerEnv) evRevOf(t *testing.T, id string) string {
	for _, e := range w.evRevs(t) {
		if e.ID == id {
			return e.Rev
		}
	}
	return ""
}

// supState returns the seen receipts (contiguous frontier plus receipts above a gap) and the reviews of supTeam as saved
// in featsup.json. It reads the file from disk, so it does not depend on the panel's internal types.
func (w *writerEnv) supState(t *testing.T) (seen struct {
	Frontier int64
	Above    []int64
}, reviewed map[string]string) {
	var f struct {
		Seen map[string]struct {
			Frontier int64
			Above    []int64
		}
		Reviewed map[string]map[string]string
	}
	json.Unmarshal([]byte(w.supBytes(t)), &f)
	return f.Seen[supTeam], f.Reviewed[supTeam]
}

// supBytes returns the raw contents of the supervisor's featsup.json, for before/after comparisons that prove a refused
// request wrote nothing.
func (w *writerEnv) supBytes(t *testing.T) string {
	w.v.e.s.sup.mu.Lock()
	defer w.v.e.s.sup.mu.Unlock()
	b, _ := os.ReadFile(w.v.e.s.sup.file)
	return string(b)
}

// seedEvents is a fixture, not route proof: it appends n events to this feature's log in featsup.json after what the
// routes already wrote (ids continue the sequence), then restarts. Everything after it goes through the routes, with the
// panel's own cap untouched. No route can emit thousands of events quickly, so the emitters themselves are exercised by
// S1-S3 and S5.
func seedEvents(w *writerEnv, n int, mk func(seq int) map[string]any) {
	w.v.e.s.sup.mu.Lock()
	f := map[string]any{}
	if raw, err := os.ReadFile(w.v.e.s.sup.file); err == nil {
		json.Unmarshal(raw, &f)
	}
	evm, _ := f["events"].(map[string]any)
	seqm, _ := f["evSeq"].(map[string]any)
	if evm == nil {
		evm, seqm = map[string]any{}, map[string]any{}
	}
	evs, _ := evm[supTeam].([]any)
	last := 0
	if x, ok := seqm[supTeam].(float64); ok {
		last = int(x)
	}
	for i := 1; i <= n; i++ {
		e := mk(last + i)
		e["id"], e["seq"], e["at"] = fmt.Sprintf("ev-%d", last+i), last+i, "2026-11-08T12:00:00Z"
		evs = append(evs, e)
	}
	evm[supTeam], seqm[supTeam] = evs, last+n
	f["events"], f["evSeq"] = evm, seqm
	raw, _ := json.Marshal(f)
	os.WriteFile(w.v.e.s.sup.file, raw, 0o600)
	w.v.e.s.sup.mu.Unlock()
	w.restart()
}

// seed fills an empty log with 5000 assign events, the panel's retention cap, for S4 and S4b.
func seed(w *writerEnv, thread, k1 string) {
	seedEvents(w, 5000, func(int) map[string]any {
		return map[string]any{"kind": "assign", "step": k1, "tab": "Builder", "thread": thread}
	})
}

// TestVMAgentSeenReviewed covers the owner's seen and reviewed routes: seen ids are bound to their own feature and a
// bad batch records nothing; the frontier only advances over contiguous ids and survives a restart; compaction at the
// retention cap keeps sparse receipts and any DECISION still awaiting review; a full log refuses new events; the owner
// page assembles events, seen and log consistently; and reviewed is explicit, stale-guarded and separate from seen.
func TestVMAgentSeenReviewed(t *testing.T) {
	plan := "## Ship\n- [ ] Export CSV @Builder\n- [ ] Write docs @Docs\n"
	// setup makes a writer env with n assign events on the first step and returns it with the thread id and the keys
	// of the two steps.
	setup := func(t *testing.T, n int) (*writerEnv, string, string, string) {
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		k1, k2 := d.Milestones[0].Steps[0].Key, d.Milestones[0].Steps[1].Key
		for i := 0; i < n; i++ {
			if c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": d.Thread}); c != 200 {
				t.Fatalf("assign: %d %v", c, out)
			}
		}
		return w, d.Thread, k1, k2
	}
	// bads renders the "bad" list of a refused seen batch as compact JSON.
	bads := func(out map[string]any) string { b, _ := json.Marshal(out["bad"]); return string(b) }

	t.Run("S1 seen is feature-bound: a bad batch records nothing and names each id with why", func(t *testing.T) {
		var bad []string
		w, _, _, _ := setup(t, 3)
		os.WriteFile(filepath.Join(w.v.e.s.sys.sockDir, "another-feature"), nil, 0o600)
		writeFile(t, filepath.Join(w.v.e.s.cfg.home, "jarvis-files", "features", "another-feature", "PLAN.md"), "## A\n- [ ] Its step @X\n")
		ok := w.v.e.s.featDossier("another-feature").Milestones[0].Steps[0].Key
		if c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": "another-feature", "step": ok, "tab": "X"}); c != 200 || out["id"] != "ev-1" {
			t.Fatalf("another feature's event: %d %v", c, out)
		}
		before := w.supBytes(t)
		c, out := w.seen(t, supTeam, "ev-1", "ev-4", "ev-x", "4")
		want := `[{"id":"ev-4","reason":"unknown"},{"id":"ev-x","reason":"malformed"},{"id":"4","reason":"malformed"}]`
		if c != 400 || bads(out) != want {
			bad = append(bad, fmt.Sprintf("mixed batch: %d bad=%s, want 400 %s (the valid ev-1 not listed)", c, bads(out), want))
		}
		if c, out := w.seen(t, "another-feature", "ev-2"); c != 400 || bads(out) != `[{"id":"ev-2","reason":"unknown"}]` {
			bad = append(bad, fmt.Sprintf("supTeam's ev-2 sent as another feature's: %d %s", c, bads(out)))
		}
		if w.supBytes(t) != before {
			bad = append(bad, "a refused batch changed featsup.json")
		}
		if c, out := w.seen(t, "another-feature", "ev-1"); c != 200 || out["frontier"] != "ev-1" || out["unseen"] != float64(0) {
			bad = append(bad, fmt.Sprintf("another feature's own ev-1: %d %v", c, out))
		}
		if c, out := w.seen(t, supTeam); c != 200 || out["frontier"] != "ev-0" || out["unseen"] != float64(3) {
			bad = append(bad, fmt.Sprintf("seeing another feature's ev-1 touched this feature: %d %v", c, out))
		}
		many := make([]string, 201)
		for i := range many {
			many[i] = "ev-1"
		}
		if c, _ := w.seen(t, supTeam, many...); c != 400 {
			bad = append(bad, fmt.Sprintf("201 ids: %d, want 400", c))
		}
		if c, _ := w.seen(t, "bad name!", "ev-1"); c != 400 {
			bad = append(bad, fmt.Sprintf("a malformed session: %d, want 400", c))
		}
		for _, path := range []string{"/api/term/feature/seen", "/api/term/feature/reviewed"} {
			req, _ := http.NewRequest("POST", w.url+path, strings.NewReader(`{"session":"`+supTeam+`","ids":["ev-1"],"id":"ev-1","rev":""}`))
			req.Header.Set("Authorization", "Bearer contract-token")
			if r, err := http.DefaultClient.Do(req); err == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					bad = append(bad, path+" accepted the agent bearer: seen/reviewed are the owner's")
				}
			}
		}
		if sn, rv := w.supState(t); len(rv) != 0 || sn.Frontier != 0 || len(sn.Above) != 0 {
			bad = append(bad, fmt.Sprintf("the agent bearer recorded seen/reviewed: %v, %+v", rv, sn))
		}
		scenario(t, "S1 seen ids are this feature's only; a bad batch (unknown/malformed/other feature's) records nothing; owner session only", &bad)
	})

	t.Run("S2 gaps, contiguous frontier, unseen, idempotent; reads mark nothing seen", func(t *testing.T) {
		var bad []string
		w, thread, k1, _ := setup(t, 5)
		check := func(why string, ids []string, front string, unseen int) {
			if c, out := w.seen(t, supTeam, ids...); c != 200 || out["frontier"] != front || out["unseen"] != float64(unseen) {
				bad = append(bad, fmt.Sprintf("%s: %d %v, want frontier %s unseen %d", why, c, out, front, unseen))
			}
		}
		// reads first: owner and agent event pages, the agent record, vm_mcp; none may record seen
		before := w.supBytes(t)
		w.events(t, "")
		w.get(t, "/agent/feature?session="+supTeam)
		w.v.e.do(t, "GET", "/api/term/feature/events?session="+supTeam, "", w.v.e.authed(w.v.e.login(t))).Body.Close()
		w.py("m.get_feature_events(sys.argv[1])", supTeam)
		if w.supBytes(t) != before {
			bad = append(bad, "a GET changed featsup.json (seen must never be set by a read)")
		}
		check("no receipts yet", nil, "ev-0", 5)
		check("ev-4 alone marks only ev-4", []string{"ev-4"}, "ev-0", 4)
		check("ev-1,ev-2", []string{"ev-2", "ev-1"}, "ev-2", 2)
		check("ev-3 twice closes the gap over ev-4", []string{"ev-3", "ev-3"}, "ev-4", 1)
		b1 := w.supBytes(t)
		check("ev-3 again: idempotent", []string{"ev-3", "ev-1"}, "ev-4", 1)
		if w.supBytes(t) != b1 {
			bad = append(bad, "re-sending seen ids changed featsup.json")
		}
		w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": thread})
		check("a new event ev-6 is unseen", nil, "ev-4", 2)
		if _, rv := w.supState(t); len(rv) != 0 {
			bad = append(bad, "seen recorded a review")
		}
		scenario(t, "S2 seen: gaps kept, frontier only over contiguous ids, unseen count, idempotent, GETs record nothing, never a review", &bad)
	})

	t.Run("S3 restart keeps the frontier and the receipts past a gap", func(t *testing.T) {
		var bad []string
		w, _, _, _ := setup(t, 6)
		w.seen(t, supTeam, "ev-1", "ev-2", "ev-5")
		w.restart()
		if c, out := w.seen(t, supTeam); c != 200 || out["frontier"] != "ev-2" || out["unseen"] != float64(3) {
			bad = append(bad, fmt.Sprintf("after a restart: %d %v, want ev-2 / 3", c, out))
		}
		if c, out := w.seen(t, supTeam, "ev-3", "ev-4"); c != 200 || out["frontier"] != "ev-5" || out["unseen"] != float64(1) {
			bad = append(bad, fmt.Sprintf("closing the gap after a restart: %d %v, want ev-5 / 1 (ev-5's receipt survived)", c, out))
		}
		scenario(t, "S3 seen frontier and above-gap receipts survive a restart", &bad)
	})

	t.Run("S4 retention at the panel's own cap: compacted ids refused, paging resumes past them, unseen stays true", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		k1 := d.Milestones[0].Steps[0].Key
		seed(w, d.Thread, k1)
		assign := func() any {
			c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": d.Thread})
			if c != 200 {
				t.Fatalf("assign: %d %v", c, out)
			}
			return out["id"]
		}
		if id := assign(); id != "ev-5001" {
			bad = append(bad, fmt.Sprintf("the first routed event after 5000: %v, want ev-5001", id))
		}
		ids := func(p evPage) string {
			var s []string
			for _, e := range p.Events {
				s = append(s, e.ID)
			}
			return strings.Join(s, ",")
		}
		if _, p := w.events(t, "&limit=2"); ids(p) != "ev-2,ev-3" || !p.More {
			bad = append(bad, fmt.Sprintf("the oldest kept after compaction to 5000: %s more=%v, want ev-2,ev-3 more", ids(p), p.More))
		}
		if _, p := w.events(t, "&after=ev-5000"); ids(p) != "ev-5001" || p.More {
			bad = append(bad, fmt.Sprintf("after ev-5000: %s more=%v", ids(p), p.More))
		}
		before := w.supBytes(t)
		if c, out := w.seen(t, supTeam, "ev-1", "ev-2"); c != 400 || bads(out) != `[{"id":"ev-1","reason":"compacted"}]` || w.supBytes(t) != before {
			bad = append(bad, fmt.Sprintf("a compacted id: %d %s (want 400 compacted for ev-1 only, nothing recorded)", c, bads(out)))
		}
		if c, out := w.owner(t, "/api/term/feature/reviewed", map[string]any{"session": supTeam, "id": "ev-1", "rev": ""}); c != 400 {
			bad = append(bad, fmt.Sprintf("reviewing a compacted id: %d %v, want 400", c, out))
		}
		// the owner sees every kept event, oldest first, 200 at a time (the page's batch limit)
		var out map[string]any
		for lo := 2; lo <= 5001; lo += 200 {
			var batch []string
			for n := lo; n < lo+200 && n <= 5001; n++ {
				batch = append(batch, fmt.Sprintf("ev-%d", n))
			}
			var c int
			if c, out = w.seen(t, supTeam, batch...); c != 200 {
				t.Fatalf("seen batch from ev-%d: %d %v", lo, c, out)
			}
		}
		if out["unseen"] != float64(0) {
			bad = append(bad, fmt.Sprintf("the owner saw all 5000 kept events but unseen = %v, want 0 (frontier %v: it can't pass compacted ev-1, and the receipts above it are capped, so the oldest seen receipts were dropped)", out["unseen"], out["frontier"]))
		}
		t.Logf("S4 observation: after seeing every kept event, frontier = %v, unseen = %v", out["frontier"], out["unseen"])
		w.restart()
		if id := assign(); id != "ev-5002" {
			bad = append(bad, fmt.Sprintf("the next id after compaction and a restart: %v, want ev-5002 (never reused)", id))
		}
		if c, out := w.seen(t, supTeam); c != 200 || out["unseen"] != float64(1) {
			bad = append(bad, fmt.Sprintf("after a restart and one new event: %d %v, want unseen 1", c, out))
		}
		scenario(t, "S4 compaction at the default cap: compacted ids 400 'compacted' (seen and reviewed), paging resumes past them, ids never reused, unseen true after seeing all", &bad)
	})

	t.Run("S4b retention floor bridges only the compacted prefix; sparse receipts are never discarded", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		k1 := d.Milestones[0].Steps[0].Key
		seed(w, d.Thread, k1)
		assign := func() {
			if c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": d.Thread}); c != 200 {
				t.Fatalf("assign: %d %v", c, out)
			}
		}
		assign() // ev-5001: ev-1 compacted, ev-2..ev-5001 kept
		want := func(why string, ids []string, unseen int) {
			for len(ids) > 0 || why != "" {
				n := min(200, len(ids))
				c, out := w.seen(t, supTeam, ids[:n]...)
				if c != 200 {
					t.Fatalf("%s: %d %v", why, c, out)
				}
				if ids = ids[n:]; len(ids) == 0 {
					if out["unseen"] != float64(unseen) {
						bad = append(bad, fmt.Sprintf("%s: unseen %v (frontier %v), want %d", why, out["unseen"], out["frontier"], unseen))
					}
					return
				}
			}
		}
		want("nothing seen after compaction: every kept event is unseen (the floor bridges no kept id)", nil, 5000)
		var sparse []string // every other kept id, oldest first: ev-3, ev-5, ... ev-5001 (2500 receipts, all past gaps)
		for n := 3; n <= 5001; n += 2 {
			sparse = append(sparse, fmt.Sprintf("ev-%d", n))
		}
		want("2500 sparse receipts (more than 1000 past gaps): none discarded", sparse, 2500)
		w.restart()
		want("the same after a restart", nil, 2500)
		assign() // ev-5002 arrives; ev-2 is compacted, unseen
		want("a new arrival after compaction moved again", nil, 2500)
		var rest []string
		for n := 4; n <= 5002; n += 2 {
			rest = append(rest, fmt.Sprintf("ev-%d", n))
		}
		want("the remaining kept ids (ev-2 now compacted): all seen", rest, 0)
		scenario(t, "S4b floor only over the truly compacted prefix (5000 unseen kept stay unseen), 2500 sparse receipts kept exactly, across a restart and a new arrival", &bad)
	})

	t.Run("S5 a typed DECISION awaiting review survives compaction; an internal compacted gap is no gap; reviewed, it goes", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		k1 := d.Milestones[0].Steps[0].Key
		if out := w.py("m.report_feature_progress(sys.argv[1], 'decision', 'docs first', step=sys.argv[2], by='Docs')", supTeam, k1); out != "OK logged (ev-1)" {
			t.Fatalf("the routed DECISION: %s", out)
		}
		assign := func() any {
			c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": d.Thread})
			if c != 200 {
				t.Fatalf("assign: %d %v", c, out)
			}
			return out["id"]
		}
		assign()                                       // ev-2
		seedEvents(w, 4998, func(int) map[string]any { // ev-3..ev-5000
			return map[string]any{"kind": "assign", "step": k1, "tab": "Builder", "thread": d.Thread}
		})
		assign() // ev-5001: the oldest event not awaiting review (ev-2) goes, the DECISION (ev-1) stays
		ids := func(p evPage) string {
			var s []string
			for _, e := range p.Events {
				s = append(s, e.ID)
			}
			return strings.Join(s, ",")
		}
		if _, p := w.events(t, "&limit=2"); ids(p) != "ev-1,ev-3" {
			bad = append(bad, fmt.Sprintf("after compaction: %s, want ev-1,ev-3 (the unreviewed DECISION kept, ev-2 dropped)", ids(p)))
		}
		if c, out := w.seen(t, supTeam, "ev-2"); c != 400 || bads(out) != `[{"id":"ev-2","reason":"compacted"}]` {
			bad = append(bad, fmt.Sprintf("the internal compacted ev-2: %d %s", c, bads(out)))
		}
		if c, out := w.seen(t, supTeam, "ev-3"); c != 200 || out["frontier"] != "ev-0" || out["unseen"] != float64(4999) || out["retainedFrom"] != "ev-1" {
			bad = append(bad, fmt.Sprintf("ev-3 seen before ev-1: %d %v, want frontier ev-0 (ev-1 unseen and retained) unseen 4999 retainedFrom ev-1", c, out))
		}
		if c, out := w.seen(t, supTeam, "ev-1"); c != 200 || out["frontier"] != "ev-3" || out["unseen"] != float64(4998) {
			bad = append(bad, fmt.Sprintf("ev-1 seen: %d %v, want the frontier over the compacted ev-2 to ev-3, unseen 4998", c, out))
		}
		w.restart()
		if c, out := w.seen(t, supTeam); c != 200 || out["frontier"] != "ev-3" || out["unseen"] != float64(4998) {
			bad = append(bad, fmt.Sprintf("after a restart: %d %v", c, out))
		}
		if _, rv := w.supState(t); len(rv) != 0 {
			bad = append(bad, fmt.Sprintf("seen reviewed the DECISION: %v", rv))
		}
		if c, out := w.owner(t, "/api/term/feature/reviewed", map[string]any{"session": supTeam, "id": "ev-1", "rev": w.evRevOf(t, "ev-1")}); c != 200 {
			bad = append(bad, fmt.Sprintf("reviewing the DECISION at its own rev: %d %v", c, out))
		}
		assign() // ev-5002: now the reviewed ev-1 is the oldest eligible
		if _, p := w.events(t, "&limit=1"); ids(p) != "ev-3" {
			bad = append(bad, fmt.Sprintf("after review and a new event: oldest kept %s, want ev-3", ids(p)))
		}
		if c, out := w.seen(t, supTeam); out["retainedFrom"] != "ev-3" || out["frontier"] != "ev-3" || out["unseen"] != float64(4999) {
			bad = append(bad, fmt.Sprintf("seen after ev-1 went: %d %v, want retainedFrom ev-3 frontier ev-3 unseen 4999", c, out))
		}
		scenario(t, "S5 an unreviewed typed DECISION (routed) survives compaction, an internal compacted id is no gap for the frontier, restart-safe; once reviewed it is compacted (5000-event FIXTURE seed)", &bad)
	})

	t.Run("S7 backpressure: twice the cap awaiting review refuses new events, said; the LOG line stands; no false receipt", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		k1 := d.Milestones[0].Steps[0].Key
		seedEvents(w, 10000, func(n int) map[string]any { // FIXTURE: 2 x 5000 typed DECISION log events awaiting review
			return map[string]any{"kind": "log", "text": fmt.Sprintf("DECISION: seeded %d", n)}
		})
		before := w.supBytes(t)
		out := w.py("m.report_feature_progress(sys.argv[1], 'decision', 'backpressure-line-1')", supTeam)
		if !strings.HasPrefix(out, "OK WARNING:") || strings.Contains(out, "ev-10001") {
			bad = append(bad, "the tool over a full event log: "+out)
		}
		if _, raw := w.get(t, "/agent/feature?session="+supTeam); !strings.Contains(string(raw), "backpressure-line-1") {
			bad = append(bad, "the refused event's LOG line isn't in LOG.md")
		}
		if c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": d.Thread}); c == 200 || out["id"] != nil {
			bad = append(bad, fmt.Sprintf("an assign over a full log: %d %v, want refused with no id", c, out))
		}
		if w.supBytes(t) != before {
			bad = append(bad, "a refused event changed featsup.json (an id spent or an event half-written)")
		}
		if c, out := w.seen(t, supTeam, "ev-10001"); c != 400 || bads(out) != `[{"id":"ev-10001","reason":"unknown"}]` {
			bad = append(bad, fmt.Sprintf("seen on the refused event's would-be id: %d %s", c, bads(out)))
		}
		if c, out := w.owner(t, "/api/term/feature/reviewed", map[string]any{"session": supTeam, "id": "ev-1", "rev": ""}); c != 200 {
			bad = append(bad, fmt.Sprintf("review ev-1: %d %v", c, out))
		}
		if out := w.py("m.report_feature_progress(sys.argv[1], 'decision', 'backpressure-line-2')", supTeam); out != "OK logged (ev-10001)" {
			bad = append(bad, "after one review the next event: "+out)
		}
		if _, p := w.events(t, "&limit=1"); len(p.Events) != 1 || p.Events[0].ID != "ev-2" {
			bad = append(bad, fmt.Sprintf("compaction past the cap took the reviewed ev-1 only: oldest %+v", p.Events))
		}
		scenario(t, "S7 2x cap awaiting review: new events refused (assign not 200, LOG 200 + warning, line kept, tool says WARNING), no id spent, no receipt possible; one review frees one (10000-event FIXTURE seed)", &bad)
	})

	t.Run("S8 page assembly: page.events tail/eventsMore, page.seen = /seen, page.log typed by claim (at unix s), reads record nothing", func(t *testing.T) {
		var bad []string
		w, _, k1, _ := setup(t, 0)
		w.py("m.report_feature_progress(sys.argv[1], 'decision', 'docs first', considered=['docs first', 'region first'], chose='docs first', step=sys.argv[2], by='@Docs')", supTeam, k1)
		w.py("m.report_feature_progress(sys.argv[1], 'action', 'ship it | chose: skip review | step: '+sys.argv[2]+' | by: @Owner')", supTeam, k1)
		w.py("m.report_feature_progress(sys.argv[1], 'progress', 'a plain note')", supTeam)
		seedEvents(w, 250, func(int) map[string]any { return map[string]any{"kind": "assign", "step": k1, "tab": "Builder"} })
		w.seen(t, supTeam, "ev-1", "ev-3")
		w.v.look() // the page's session listing
		before := w.supBytes(t)
		r := w.v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", w.v.e.authed(w.v.e.login(t)))
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("the owner's page GET: %d %s", r.StatusCode, raw)
		}
		var f struct {
			Page struct {
				Events     []struct{ ID, At string }
				EventsMore bool
				Seen       struct {
					Frontier     int64
					IDs          []string
					Unseen       int
					RetainedFrom string
				}
				Log []struct {
					ID, Who, Kind, Title, Step string
					At                         int64
					Review                     bool
					Considered                 []struct{ Option string }
					Chose                      *struct{ Option string }
				}
			}
		}
		json.Unmarshal(raw, &f)
		p := f.Page
		if len(p.Events) != 200 || !p.EventsMore || p.Events[0].ID != "ev-54" || p.Events[199].ID != "ev-253" {
			bad = append(bad, fmt.Sprintf("page.events: %d, more %v (want the last 200, ev-54..ev-253, more)", len(p.Events), p.EventsMore))
		}
		if _, out := w.seen(t, supTeam); p.Seen.Unseen != int(out["unseen"].(float64)) || p.Seen.RetainedFrom != out["retainedFrom"] || fmt.Sprintf("ev-%d", p.Seen.Frontier) != out["frontier"] || p.Seen.Frontier != 1 || strings.Join(p.Seen.IDs, ",") != "ev-3" {
			bad = append(bad, fmt.Sprintf("page.seen %+v differs from /seen %v", p.Seen, out))
		}
		if len(p.Log) != 2 {
			bad = append(bad, fmt.Sprintf("page.log has %d entries, want 2 typed lines (the plain note is not typed)", len(p.Log)))
		} else {
			dl, al := p.Log[0], p.Log[1]
			_, b := w.get(t, "/agent/feature/events?session="+supTeam+"&limit=1")
			var x struct{ Events []struct{ ID, At string } }
			json.Unmarshal(b, &x)
			if len(x.Events) == 0 || x.Events[0].ID != dl.ID {
				bad = append(bad, fmt.Sprintf("page.log's DECISION %s isn't the ledger's first event %s", dl.ID, b))
			} else if tm, err := time.Parse(time.RFC3339, x.Events[0].At); err != nil || dl.At != tm.Unix() {
				bad = append(bad, fmt.Sprintf("page.log at %d (unix s) vs the ledger's at %q (RFC 3339): %v", dl.At, x.Events[0].At, err))
			}
			if dl.Kind != "DECISION" || !dl.Review || dl.Who != "Docs" || dl.Step != k1 || len(dl.Considered) != 2 || dl.Chose == nil || dl.Chose.Option != "docs first" {
				bad = append(bad, fmt.Sprintf("the DECISION entry: %+v", dl))
			}
			if al.Kind != "ACTION" || al.Review || al.Who != "unknown" || al.Step != "" || al.Chose != nil {
				bad = append(bad, fmt.Sprintf("the forged ACTION: delimiters in its text became fields (who/step/chose): %+v", al))
			}
		}
		if w.supBytes(t) != before {
			bad = append(bad, "the owner's page GET changed featsup.json (no seen on view)")
		}
		scenario(t, "S8 page: last 200 events + eventsMore, page.seen = /seen, page.log typed lines only, who by claim else unknown, at = ledger at in unix s, forged delimiters stay text, the GET records nothing", &bad)
	})

	t.Run("S6 reviewed: explicit, stale-guarded (rev, newer same-kind event on the step), persists, never marks seen", func(t *testing.T) {
		var bad []string
		w, thread, k1, k2 := setup(t, 2)                                                                                         // ev-1, ev-2: assign on k1
		w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k2, "tab": "Docs", "thread": thread}) // ev-3 on k2
		rev := func(id, r string) (int, map[string]any) {
			return w.owner(t, "/api/term/feature/reviewed", map[string]any{"session": supTeam, "id": id, "rev": r})
		}
		before := w.supBytes(t)
		if c, out := rev("ev-1", w.evRevOf(t, "ev-1")); c != 409 || out["changed"] != "ev-2" {
			bad = append(bad, fmt.Sprintf("ev-1 superseded by ev-2 on the same step: %d %v, want 409 changed ev-2", c, out))
		}
		if c, out := rev("ev-3", "stale"); c != 409 || out["changed"] != "ev-3" || out["rev"] != w.evRevOf(t, "ev-3") {
			bad = append(bad, fmt.Sprintf("a rev that isn't the event's: %d %v, want 409 changed ev-3 with its own rev", c, out))
		}
		for _, id := range []string{"ev-9", "ev-0", "x", ""} {
			if c, _ := rev(id, ""); c != 400 {
				bad = append(bad, fmt.Sprintf("review %q: %d, want 400", id, c))
			}
		}
		if w.supBytes(t) != before {
			bad = append(bad, "a refused review changed featsup.json")
		}
		if c, out := rev("ev-2", w.evRevOf(t, "ev-2")); c != 200 {
			bad = append(bad, fmt.Sprintf("review ev-2: %d %v", c, out))
		}
		if c, out := rev("ev-3", w.evRevOf(t, "ev-3")); c != 200 {
			bad = append(bad, fmt.Sprintf("review ev-3 (another step: ev-2 doesn't supersede it): %d %v", c, out))
		}
		w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": k1, "tab": "Builder", "thread": thread}) // ev-4 on k1
		if c, out := rev("ev-2", w.evRevOf(t, "ev-2")); c != 409 || out["changed"] != "ev-4" {
			bad = append(bad, fmt.Sprintf("re-review ev-2 after ev-4 on its step: %d %v, want 409 changed ev-4", c, out))
		}
		w.restart()
		sn, got := w.supState(t)
		_, r2 := got["ev-2"]
		_, r3 := got["ev-3"]
		if !r2 || !r3 || len(got) != 2 {
			bad = append(bad, fmt.Sprintf("reviews as saved (featsup.json; no read route yet): %v", got))
		}
		if sn.Frontier != 0 || len(sn.Above) != 0 {
			bad = append(bad, fmt.Sprintf("reviewing marked seen: %+v", sn))
		}
		if c, out := w.seen(t, supTeam); out["unseen"] != float64(4) {
			bad = append(bad, fmt.Sprintf("unseen after reviews: %d %v, want 4", c, out))
		}
		scenario(t, "S6 reviewed: 409 changed on a rev mismatch or a newer same-kind event for the step, 400 bad id, persists, separate from seen", &bad)
	})
}
