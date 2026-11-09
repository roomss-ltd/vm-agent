package main

// Behaviour guards for note delivery, supervisor events, the owner's verification and acceptance, and the done gate.
// Each asserts the CORRECT behaviour through the real paths where they exist: a Hermes this suite controls, the owner's
// page routes, the workers' bin/feature-doc and vm_mcp.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// vmDone calls vm_mcp's thread_update(status=done) through the agent API, as a lead would. It reports whether the call
// was accepted, with the tool's output.
func (w *writerEnv) vmDone(id string) (bool, string) {
	c := exec.Command("uv", "run", "--quiet", "--with", "mcp>=2.3,<3", "python", "-c",
		"import sys,vm_mcp as m\ntry:\n print('OK', m.thread_update(sys.argv[1], status='done'))\nexcept m.PanelError as e:\n print('REFUSED', e)", id)
	c.Dir = filepath.Join(w.vm, "mcp")
	c.Env = append(os.Environ(), "PANEL_AGENT_URL="+w.url, "PANEL_AGENT_TOKEN_FILE="+w.tok)
	b, _ := c.CombinedOutput()
	return strings.HasPrefix(string(b), "OK"), strings.TrimSpace(string(b))
}

// ownerVerify performs the owner's "Mark verified" on the page for step i, at the plan revision the page shows, and
// returns the HTTP status.
func (w *writerEnv) ownerVerify(t *testing.T, i int) int {
	d := w.v.e.s.featDossier(supTeam)
	b, _ := json.Marshal(map[string]string{"session": supTeam, "key": d.Milestones[0].Steps[i].Key, "rev": d.Rev})
	return w.v.e.do(t, "POST", "/api/term/feature/verify", string(b), w.v.e.authed(w.v.e.login(t))).StatusCode
}

// step returns step i of the feature's first milestone, as the owner's page sees it.
func (w *writerEnv) step(i int) featPlanStep {
	return w.v.e.s.featDossier(supTeam).Milestones[0].Steps[i]
}

// TestVMAgentFollowup checks that delivery never replays an unknown outcome and never loses a note, that a
// supervisor event survives a failed save, that the owner's verification and an uncertain mutation's hold end only
// when they should, that done needs the owner's acceptance of the exact outcomes, and that the done check and its
// commit are one transaction with the record's writers.
func TestVMAgentFollowup(t *testing.T) {
	t.Run("F1 an unknown outcome is never replayed while the lead's history can't be read", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.f.set("drop") // Hermes keeps it, the answer is lost: the panel can't know
		id := v.news("critical delivery")
		v.tick(time.Second)
		if ok, why := v.dispatched(id); !ok {
			bad = append(bad, why)
		}
		v.f.mu.Lock()
		v.f.histDown = true // and now the history answers 503, while turns still work
		v.f.mu.Unlock()
		v.f.set("ok")
		for range 20 {
			v.tick(10 * time.Minute)
			time.Sleep(20 * time.Millisecond)
		}
		if n := v.f.kept(id); n != 1 {
			bad = append(bad, fmt.Sprintf("with the history unreadable it was sent again: %d copies kept", n))
		}
		v.f.mu.Lock()
		v.f.histDown = false
		v.f.mu.Unlock()
		if !v.settle(func() bool { return v.seen() >= id }) || v.f.kept(id) != 1 {
			bad = append(bad, fmt.Sprintf("once the history was readable: cursor %d (note %d), copies %d", v.seen(), id, v.f.kept(id)))
		}
		scenario(t, "F1 unknown delivery + history 503: no replay; acknowledged from history once readable, one copy", &bad)
	})

	t.Run("F2 a supervisor event whose note couldn't be saved is not lost", func(t *testing.T) {
		var bad []string
		v := newSupEnv(t, "")
		parent := filepath.Join(t.TempDir(), "not-a-directory")
		writeFile(t, parent, "x")
		good := v.e.s.notes.file
		v.e.s.notes.file = filepath.Join(parent, "notes.json") // the notes can't be saved; the supervisor's state can
		v.a.mu.Lock()
		th := *v.a.thread(supTeam)
		v.a.mu.Unlock()
		v.e.s.raise(v.now, &th, true, "lost-critical", "bad", "critical blocker", "must persist")
		v.e.s.sup = newFeatSup(v.e.s.sup.file) // restart: the supervisor from disk, the notes as they were saved
		v.e.s.notes = newNotifier(good)
		v.e.s.raise(v.now, &th, true, "lost-critical", "bad", "critical blocker", "must persist")
		found := false
		for _, n := range v.e.s.notes.since(0) {
			found = found || strings.Contains(n.Title, "critical blocker")
		}
		if !found {
			bad = append(bad, "after the failed save and a restart the blocker is gone for good (marked sent, never saved)")
		}
		scenario(t, "F2 failed note save: the event isn't marked sent; raised again after a restart", &bad)
	})

	t.Run("F3 a restart keeps every undelivered note, even one never put in the outbox", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.f.set("503")
		first := v.news("in the outbox")
		v.tick(time.Second)
		if ok, why := v.dispatched(first); !ok {
			bad = append(bad, why)
		}
		backlog := v.news("backlog 0, not yet in the outbox")
		for i := 1; i <= maxNotes+5; i++ {
			v.news(fmt.Sprintf("backlog %d", i))
		}
		v.restart()
		v.news("one more, before any delivery after the restart") // observation adds notes before teamDeliver runs
		have := map[int64]bool{}
		for _, n := range v.e.s.notes.since(0) {
			have[n.ID] = true
		}
		if !have[first] || !have[backlog] {
			bad = append(bad, fmt.Sprintf("undelivered notes were dropped by the cap after the restart (outbox note kept %v, first backlog note kept %v)", have[first], have[backlog]))
		}
		v.f.set("ok")
		if !v.settle(func() bool { return v.f.kept(first) == 1 && v.f.kept(backlog) == 1 }) {
			bad = append(bad, fmt.Sprintf("the lead never got them: outbox note %d copies, backlog note %d copies", v.f.kept(first), v.f.kept(backlog)))
		}
		scenario(t, "F3 restart retention: no undelivered note is lost, each delivered once", &bad)
	})

	t.Run("F4 the owner's verification ends when the step's check, cwd or owner changes", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [x] Check target @Builder\n  - check: test sandbox\n  - cwd: /sandbox\n")
		if c := w.ownerVerify(t, 0); c != 200 || w.step(0).State != "done" {
			t.Fatalf("precondition: the owner's verify answered %d, step %+v", c, w.step(0))
		}
		cur, _ := os.ReadFile(w.v.planFile())
		changed := strings.ReplaceAll(strings.ReplaceAll(string(cur), "sandbox", "production"), "@Builder", "@Other")
		if c, out := w.fdoc(changed, "put", "plan", "-", "--rev", w.rev(t, "plan")); c != 0 {
			t.Fatalf("precondition: the worker's write failed: %s", out)
		}
		if st := w.step(0); st.State == "done" || st.Label == "owner verified" {
			bad = append(bad, fmt.Sprintf("still %s/%q after the check moved from sandbox to production", st.State, st.Label))
		}
		if ok, out := w.vmDone("demo-feature"); ok {
			bad = append(bad, "vm_mcp's done was accepted: "+out)
		}
		scenario(t, "F4 changed check/cwd/owner voids the owner's verification; done refused", &bad)
	})

	t.Run("F5 an uncertain mutation's hold survives a change of @owner", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [~] Change DNS @Builder\n  - kind: mutation\n")
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		w.v.look()
		w.v.look() // Builder exited mid-mutation: whether DNS changed is unknown
		if w.step(0).Blocked == "" {
			t.Fatalf("precondition: no hold after the exit: %+v", w.step(0))
		}
		if c, out := w.fdoc("## Ship\n- [x] Change DNS @Other\n  - kind: mutation\n", "put", "plan", "-", "--rev", w.rev(t, "plan")); c != 0 {
			t.Fatalf("precondition: the worker's write failed: %s", out)
		}
		if st := w.step(0); st.Blocked == "" {
			bad = append(bad, "reassigning the step to @Other dropped the hold")
		}
		if c := w.ownerVerify(t, 0); c == 200 {
			bad = append(bad, "the owner could mark it verified without releasing the hold")
		}
		if ok, out := w.vmDone("demo-feature"); ok {
			bad = append(bad, "vm_mcp's done was accepted: "+out)
		}
		waitIdle(t, w.v.a, supTeam)
		scenario(t, "F5 hold on an uncertain mutation survives reassignment; no verify or done until released", &bad)
	})

	t.Run("F6 an open desired outcome keeps the feature from done until the owner accepts that exact set", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [x] Read a log\n  - covers: D1, D2\n") // ids and a covering step: id-less or uncovered outcomes block done
		fpath := filepath.Join(filepath.Dir(w.v.planFile()), "FEATURE.md")
		writeFile(t, fpath, "# Desired outcome\n- [ ] D1: Production is healthy\n")
		if c := w.ownerVerify(t, 0); c != 200 || w.step(0).State != "done" {
			t.Fatalf("precondition: the owner's verify answered %d, step %+v", c, w.step(0))
		}
		if ok, out := w.vmDone("demo-feature"); ok {
			bad = append(bad, "done accepted with 'Production is healthy' not accepted by the owner: "+out)
		}
		// the agent bearer can't accept, on either server or path
		agent := httptest.NewServer(w.v.e.s.agentHandler("agent-token"))
		defer agent.Close()
		frev := w.v.e.s.featDossier(supTeam).Revs["feature"]
		body := `{"session":"demo-feature","rev":"` + frev + `"}`
		for _, u := range []string{agent.URL + "/api/term/feature/accept", agent.URL + "/agent/feature/accept", w.v.e.srv.URL + "/api/term/feature/accept"} {
			req, _ := http.NewRequest("POST", u, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer agent-token")
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode < 400 {
					bad = append(bad, fmt.Sprintf("%s answered %d to the agent bearer", u, resp.StatusCode))
				}
			}
		}
		if ok, _ := w.vmDone("demo-feature"); ok {
			bad = append(bad, "done accepted after the agent bearer tried to accept")
		}
		accept := func(rev string) int {
			return w.v.e.do(t, "POST", "/api/term/feature/accept", `{"session":"demo-feature","rev":"`+rev+`"}`, w.v.e.authed(w.v.e.login(t))).StatusCode
		}
		if c := accept("stale-rev"); c != 409 {
			bad = append(bad, fmt.Sprintf("the owner's accept at a stale FEATURE rev answered %d, not 409", c))
		}
		if c := accept(frev); c != 200 {
			bad = append(bad, fmt.Sprintf("the owner's accept answered %d", c))
			scenario(t, "F6 outcomes: done refused until the owner (only) accepts that exact set; a changed set voids it", &bad)
			return
		}
		// scope: a changed outcome set voids the acceptance
		writeFile(t, fpath, "# Desired outcome\n- [ ] D1: Production is healthy\n- [ ] D2: Rollback tested\n")
		if ok, out := w.vmDone("demo-feature"); ok {
			bad = append(bad, "done accepted after an outcome was added since the owner's acceptance: "+out)
		}
		if c := accept(frev); c != 409 {
			bad = append(bad, fmt.Sprintf("an accept at the pre-change FEATURE rev answered %d, not 409", c))
		}
		if c := accept(w.v.e.s.featDossier(supTeam).Revs["feature"]); c != 200 {
			bad = append(bad, fmt.Sprintf("the owner's re-accept answered %d", c))
		}
		if ok, out := w.vmDone("demo-feature"); !ok {
			bad = append(bad, "done refused after the owner accepted the current outcomes: "+out)
		}
		scenario(t, "F6 outcomes: done refused until the owner (only) accepts that exact set; a changed set voids it", &bad)
	})

	t.Run("F7 the done check and its commit are one transaction with the record's writers", func(t *testing.T) {
		// featDone runs under featPlanMu, so a writer started from the hook must wait until the commit, and lands
		// afterwards as a later edit. A writer that never finishes is a failure.
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [x] Check output\n")
		if c := w.ownerVerify(t, 0); c != 200 {
			t.Fatalf("precondition: the owner's verify answered %d", c)
		}
		if why := vmAcceptOutcomes(t, w.v, supTeam); why != "" {
			t.Fatal("precondition: " + why)
		}
		// the hook runs on the server's goroutine under a.mu (updateThread holds it): its state is set and read under a.mu
		w.v.a.mu.Lock()
		orig := w.v.a.featDone
		wrote := make(chan error, 1)
		started, early := false, false
		var hookBad []string
		w.v.a.featDone = func(th *thread, p []byte) string {
			why := orig(th, p)
			if started {
				return why
			}
			started = true
			b, _ := os.ReadFile(w.v.planFile())
			go func() { wrote <- featWriteRev(w.v.planFile(), featRev(b), []byte("## Ship\n- [ ] Check output\n")) }()
			select {
			case err := <-wrote:
				hookBad = append(hookBad, fmt.Sprintf("a writer finished between the done check and the commit (err %v)", err))
				early = true
			case <-time.After(500 * time.Millisecond):
			}
			return why
		}
		w.v.a.mu.Unlock()
		ok, out := w.vmDone("demo-feature")
		w.v.a.mu.Lock()
		bad = append(bad, hookBad...)
		hookRan, hookEarly := started, early
		w.v.a.mu.Unlock()
		if !hookRan {
			t.Fatal("precondition: the featDone hook never ran")
		}
		if !ok {
			bad = append(bad, "done refused for a verified plan: "+out)
		}
		w.v.a.mu.Lock()
		st := w.v.a.thread(supTeam).Status
		w.v.a.mu.Unlock()
		if st != "done" {
			bad = append(bad, "status after the response: "+st)
		}
		if hookEarly {
			scenario(t, "F7 done check + commit hold the record lock; a concurrent writer lands only afterwards", &bad)
			return
		}
		select {
		case err := <-wrote:
			if err != nil {
				bad = append(bad, "the writer after the commit failed: "+err.Error())
			}
			if b, _ := os.ReadFile(w.v.planFile()); !strings.Contains(string(b), "- [ ] Check output") {
				bad = append(bad, "the later edit isn't in PLAN.md")
			}
		case <-time.After(15 * time.Second):
			bad = append(bad, "the writer never finished after the done response (deadlock or lost write)")
		}
		scenario(t, "F7 done check + commit hold the record lock; a concurrent writer lands only afterwards", &bad)
	})
}
