package main

// Tests for the reconcile wake. An idle working team with steps that can move wakes its lead through the durable
// outbox with a message headed "[Record snapshot as of <read time>]". featSup.Nudges[thread] = {fp: planRev/featureRev,
// at, n} is persisted; the wake fires first on a new fingerprint, then backs off from 45 min to 6 h. Time is a fixture
// (v.now and file mtimes), so nothing waits real minutes.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// idleAt makes every record file and agent quiet since `since`, then runs one supervisor look at v.now with these tabs.
func idleAt(t *testing.T, v *supEnv, since time.Time, tabs ...TTab) {
	t.Helper()
	for _, n := range []string{"PLAN.md", "LOG.md", "TESTS.md", "FEATURE.md"} {
		os.Chtimes(filepath.Join(filepath.Dir(v.planFile()), n), since, since)
	}
	for i := range tabs {
		for j := range tabs[i].Panes {
			if a := tabs[i].Panes[j].Agent; a != nil {
				a.LastAt, tabs[i].Panes[j].Since = since.Unix(), since.Unix()
			}
		}
	}
	waitIdle(t, v.a, supTeam)
	tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: tabs}}}
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list, v.e.s.term.lastAt = tm, tm, v.now
	v.e.s.term.mu.Unlock()
	v.e.s.observeTeams(v.now, tm, CI{}, Resources{})
	waitIdle(t, v.a, supTeam)
}

// wakePane returns a Claude pane with a fake agent session id in the given state.
func wakePane(id int, sid, state string) TPane {
	return TPane{ID: id, Kind: "claude", State: state, Agent: &TAgent{PID: 40 + id, sid: sid}}
}

// briefed runs the supervisor's first look, which types the record into each new agent (counted as activity); the wake
// tests start after it.
func briefed(t *testing.T, v *supEnv, tabs ...TTab) {
	t.Helper()
	tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: tabs}}}
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list, v.e.s.term.lastAt = tm, tm, v.now
	v.e.s.term.mu.Unlock()
	v.e.s.observeTeams(v.now, tm, CI{}, Resources{})
	waitIdle(t, v.a, supTeam)
}

// wakeNotes counts the wake notes raised so far and the owner-facing (thread) notes.
func wakeNotes(v *supEnv) (wakes, ownerFacing int) {
	for _, n := range v.notes("") {
		if strings.Contains(n.Title, "can move") {
			wakes++
		}
		if n.Kind == "thread" {
			ownerFacing++
		}
	}
	return
}

// TestVMAgentReconcileWake checks when the reconcile wake fires and when it stays quiet: no edit or callback needed,
// the debounce across restarts and re-plans, busy or asking tabs not hiding idle work, and stable retried payloads.
func TestVMAgentReconcileWake(t *testing.T) {
	// builder is a Builder tab with one pane in the given state.
	builder := func(state string) TTab {
		return TTab{ID: 1, Name: "Builder", Panes: []TPane{wakePane(3, "aaaa-bbbb-1", state)}}
	}
	// start builds a supervised team around plan and runs its first (briefing) look.
	start := func(t *testing.T, plan string, tabs ...TTab) *supEnv {
		v := newSupEnv(t, plan)
		briefed(t, v, tabs...)
		return v
	}

	t.Run("K1 an idle team wakes its lead with no edit or callback; the record as it is now; nothing for the owner", func(t *testing.T) {
		var bad []string
		v := start(t, "## Ship\n- [ ] Write docs @Builder\n", builder("waiting"))
		_, owner0 := wakeNotes(v)
		plan0, _ := os.ReadFile(v.planFile())
		turns0 := len(v.lead(t))
		v.now = v.now.Add(time.Hour)
		idleAt(t, v, v.now.Add(-time.Hour), builder("waiting"))
		w, owner := wakeNotes(v)
		if w != 1 {
			bad = append(bad, fmt.Sprintf("%d wakes, want 1", w))
		}
		if owner != owner0 {
			bad = append(bad, fmt.Sprintf("the wake made %d owner-facing (thread) notes", owner-owner0))
		}
		if b, _ := os.ReadFile(v.planFile()); string(b) != string(plan0) {
			bad = append(bad, "the wake edited PLAN.md")
		}
		lead := v.lead(t)
		if len(lead) <= turns0 {
			bad = append(bad, "no lead turn")
		} else if l := lead[len(lead)-1]; !strings.Contains(l, "[Record snapshot as of ") || !strings.Contains(l, "Write docs") || !strings.Contains(l, v.e.s.featDossier(supTeam).Rev) {
			bad = append(bad, "the wake's turn lacks the current record (rev, step): "+l)
		}
		scenario(t, "K1 wake without edit/callback, current record, lead-only", &bad)
	})

	t.Run("K2 a newer record isn't lost behind a hand-off still in the outbox (delivery + payload; not wake-specific)", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		plan := filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", rfeLead, "PLAN.md")
		writeFile(t, plan, "## Ship\n- [ ] Step A @Builder\n")
		v.f.set("503")
		wake := func(k int) int64 {
			n, err := v.e.s.notes.addSaved(v.now, "feature", "warn", "🧵 RFE lead: idle with 1 step(s) that can move", "Reconcile.", "thread:"+rfeLead, fmt.Sprintf("%s/stagnant:%d", rfeLead, k))
			if err != nil {
				t.Fatal(err)
			}
			return n.ID
		}
		wake(1)
		v.tick(time.Second)
		b, _ := os.ReadFile(plan)
		if err := featWriteRev(plan, featRev(b), []byte("## Ship\n- [x] Step A @Builder\n- [ ] Step B @Builder\n")); err != nil {
			t.Fatal(err)
		}
		newRev := featRev([]byte("## Ship\n- [x] Step A @Builder\n- [ ] Step B @Builder\n"))
		id2 := wake(2)
		v.f.set("ok")
		if !v.settle(func() bool { return v.seen() >= id2 }) {
			bad = append(bad, fmt.Sprintf("the second wake was never acknowledged (cursor %d, note %d)", v.seen(), id2))
		}
		v.f.mu.Lock()
		hist := append([]string(nil), v.f.hist["thread-"+rfeLead]...)
		v.f.mu.Unlock()
		if len(hist) == 0 || !strings.Contains(hist[len(hist)-1], "Step B") || !strings.Contains(hist[len(hist)-1], newRev) {
			bad = append(bad, fmt.Sprintf("the lead's newest kept message doesn't carry the newest record (rev %s, Step B): %q", newRev, hist))
		}
		scenario(t, "K2 newest canonical record reaches the lead after a pending hand-off", &bad)
	})

	t.Run("K3 a restart doesn't wake again on the same record; the pause is persisted", func(t *testing.T) {
		var bad []string
		v := start(t, "## Ship\n- [ ] Write docs @Builder\n", builder("waiting"))
		v.now = v.now.Add(time.Hour)
		idleAt(t, v, v.now.Add(-time.Hour), builder("waiting"))
		if b, _ := os.ReadFile(v.e.s.sup.file); !strings.Contains(string(b), `"nudges"`) || !strings.Contains(string(b), v.e.s.featDossier(supTeam).Rev) {
			bad = append(bad, "the wake's fingerprint isn't in the saved supervisor state")
		}
		v.e.s.sup = newFeatSup(v.e.s.sup.file)
		for i := 0; i < 5; i++ { // looks every 31 s after the restart
			v.now = v.now.Add(31 * time.Second)
			idleAt(t, v, v.now.Add(-time.Hour), builder("waiting"))
		}
		if w, _ := wakeNotes(v); w != 1 {
			bad = append(bad, fmt.Sprintf("%d wakes after a restart on the same record, want 1", w))
		}
		v.now = v.now.Add(46 * time.Minute)
		idleAt(t, v, v.now.Add(-2*time.Hour), builder("waiting"))
		if w, _ := wakeNotes(v); w != 2 {
			bad = append(bad, fmt.Sprintf("%d wakes after the 45 min pause, want 2", w))
		}
		scenario(t, "K3 restart debounce: persisted fingerprint and pause", &bad)
	})

	t.Run("K4 no self-loop: the lead re-planning after the wake (a new fingerprint) restarts the quiet period", func(t *testing.T) {
		// A plan write changes the fingerprint, which resets the backoff: only the quiet period then stands between the
		// lead's own write and the next wake. So: none for 44 min after the write, one after 46 min (not silenced forever).
		var bad []string
		v := start(t, "## Ship\n- [ ] Write docs @Builder\n", builder("waiting"))
		v.now = v.now.Add(time.Hour)
		idleAt(t, v, v.now.Add(-time.Hour), builder("waiting"))
		b, _ := os.ReadFile(v.planFile())
		if err := featWriteRev(v.planFile(), featRev(b), []byte("## Ship\n- [ ] Write docs @Builder\n- [ ] Review docs @Builder\n")); err != nil {
			t.Fatal(err)
		}
		wrote := v.now
		for i := 0; i < 88; i++ { // 44 min of looks every 30 s, quiet since the lead's write
			v.now = v.now.Add(30 * time.Second)
			idleAt(t, v, wrote, builder("waiting"))
		}
		if w, _ := wakeNotes(v); w != 1 {
			bad = append(bad, fmt.Sprintf("%d wakes within 44 min of the lead's own re-plan, want 1 (the first)", w))
		}
		v.now = wrote.Add(46 * time.Minute)
		idleAt(t, v, wrote, builder("waiting"))
		if w, _ := wakeNotes(v); w != 2 {
			bad = append(bad, fmt.Sprintf("%d wakes 46 min after the re-plan, want 2 (the new record, after its quiet period)", w))
		}
		scenario(t, "K4 the lead's write restarts the quiet period; no loop, not silenced", &bad)
	})

	// mixed is the shared body of K5 and K6: a busy or asking tab is left alone, and an idle tab elsewhere still wakes
	// the lead. The wake types into or interrupts neither tab, and approves nothing.
	mixed := func(t *testing.T, name string, busy TPane) {
		var bad []string
		docs := func() TTab { return TTab{ID: 2, Name: "Docs", Panes: []TPane{wakePane(4, "cccc-dddd-2", "waiting")}} }
		bld := func(p TPane) TTab { return TTab{ID: 1, Name: "Builder", Panes: []TPane{p}} }
		v := start(t, "## Ship\n- [?] Pick a region @Builder\n  - ask: eu or us?\n- [~] Nightly build @Builder\n- [ ] Write docs @Docs\n",
			bld(wakePane(3, "aaaa-bbbb-1", "waiting")), docs())
		typed0, appr0 := len(v.typed()), fmt.Sprint(v.e.s.featDossier(supTeam).Milestones)
		turns0 := len(v.lead(t))
		v.now = v.now.Add(time.Hour)
		busy.Since = v.now.Unix()      // the busy tab is active now; Docs has been quiet an hour
		quiet := v.now.Add(-time.Hour) // the record quiet an hour; ONE look follows, with both lanes in it
		for _, n := range []string{"PLAN.md", "LOG.md", "TESTS.md", "FEATURE.md"} {
			os.Chtimes(filepath.Join(filepath.Dir(v.planFile()), n), quiet, quiet)
		}
		waitIdle(t, v.a, supTeam)
		tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: []TTab{bld(busy), docs()}}}}
		for _, tb := range tm.Sessions[0].Tabs {
			if tb.Name == "Docs" {
				for k := range tb.Panes {
					tb.Panes[k].Agent.LastAt, tb.Panes[k].Since = v.now.Add(-time.Hour).Unix(), v.now.Add(-time.Hour).Unix()
				}
			}
		}
		busy.Agent.LastAt = v.now.Unix()
		v.e.s.term.mu.Lock()
		v.e.s.term.last, v.e.s.term.list, v.e.s.term.lastAt = tm, tm, v.now
		v.e.s.term.mu.Unlock()
		v.e.s.observeTeams(v.now, tm, CI{}, Resources{})
		waitIdle(t, v.a, supTeam)
		if w, _ := wakeNotes(v); w != 1 {
			bad = append(bad, fmt.Sprintf("%d wakes: the %s Builder lane hid the idle Docs lane", w, name))
		}
		docsKey := v.e.s.featDossier(supTeam).Milestones[0].Steps[2].Key
		if lead := v.lead(t); len(lead) <= turns0 || !strings.Contains(lead[len(lead)-1], docsKey) || !strings.Contains(lead[len(lead)-1], "Write docs") {
			bad = append(bad, "the wake doesn't name the idle Docs step (key "+docsKey+")")
		}
		if n := len(v.typed()); n != typed0 {
			bad = append(bad, fmt.Sprintf("the wake typed into or interrupted a pane: %v", v.typed()[typed0:]))
		}
		if fmt.Sprint(v.e.s.featDossier(supTeam).Milestones) != appr0 {
			bad = append(bad, "a step's state or approval changed with the wake")
		}
		scenario(t, "lanes: "+name+" Builder + idle Docs wakes for Docs; no typing, no approval", &bad)
	}
	t.Run("K5 an owner dialog in Builder doesn't hide idle Docs work", func(t *testing.T) {
		mixed(t, "K5 asking", wakePane(3, "aaaa-bbbb-1", "asking"))
	})
	t.Run("K6 a healthy long job in Builder (working, CPU 95) doesn't hide idle Docs work", func(t *testing.T) {
		p := wakePane(3, "aaaa-bbbb-1", "working")
		p.CPU = 95
		mixed(t, "K6 busy", p)
	})

	t.Run("K7 a retried hand-off: a timestamped snapshot, a read-the-current-record instruction, the same payload every try", func(t *testing.T) {
		// The contract: a "[Record snapshot as of <read time>]" label plus an instruction to read the current record
		// (feature_record / get_feature_plan) before acting, because the hand-off may be retried. A retry keeps its
		// identity and payload: no rewrite at request time, even after the record changed.
		var bad []string
		v := newRFEEnv(t)
		plan := filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", rfeLead, "PLAN.md")
		writeFile(t, plan, "## Ship\n- [ ] Step A @Builder\n")
		v.f.set("503")
		n, err := v.e.s.notes.addSaved(v.now, "feature", "warn", "🧵 RFE lead: idle with 1 step(s) that can move", "Reconcile.", "thread:"+rfeLead, rfeLead+"/stagnant:1")
		if err != nil {
			t.Fatal(err)
		}
		if ok, why := v.dispatched(n.ID); !ok {
			t.Fatal("precondition: " + why)
		}
		b, _ := os.ReadFile(plan)
		if err := featWriteRev(plan, featRev(b), []byte("## Ship\n- [x] Step A @Builder\n- [ ] Step B @Builder\n")); err != nil {
			t.Fatal(err)
		}
		v.f.set("ok")
		if !v.settle(func() bool { return v.seen() >= n.ID }) {
			bad = append(bad, "the hand-off was never acknowledged")
		}
		v.f.mu.Lock()
		var tries []string
		for _, m := range v.f.tries["thread-"+rfeLead] {
			if strings.Contains(m, fmt.Sprintf("#%d ", n.ID)) {
				tries = append(tries, m)
			}
		}
		v.f.mu.Unlock()
		if len(tries) < 2 {
			bad = append(bad, fmt.Sprintf("precondition: %d tries, want a retry", len(tries)))
		}
		for i, m := range tries {
			if m != tries[0] {
				bad = append(bad, fmt.Sprintf("try %d differs from the first (rewritten at request time)", i+1))
			}
		}
		if len(tries) > 0 {
			m := tries[0]
			if strings.Contains(m, "[The record now]") {
				bad = append(bad, "the snapshot is labelled 'now'")
			}
			if !regexp.MustCompile(`\[Record snapshot as of [^\]]*\d\d:\d\d`).MatchString(m) {
				bad = append(bad, "no '[Record snapshot as of <read time>]' label")
			}
			if !strings.Contains(m, "feature_record") && !strings.Contains(m, "get_feature_plan") {
				bad = append(bad, "no instruction to read the current record (feature_record / get_feature_plan) before acting")
			}
			if strings.Contains(m, "Step B") {
				bad = append(bad, "the retried payload carries the later record (rewritten under the same identity)")
			}
		}
		scenario(t, "K7 retried hand-off: timestamped snapshot + fresh-read instruction, stable payload", &bad)
	})

	t.Run("K8 an owner question on one step (thread needs_you) doesn't stop the wake for another idle step", func(t *testing.T) {
		// needs_you is scoped to the step with the question: reconcile also runs for a needs_you thread that has other
		// movable idle steps.
		var bad []string
		docs := func() TTab { return TTab{ID: 2, Name: "Docs", Panes: []TPane{wakePane(4, "cccc-dddd-2", "waiting")}} }
		v := start(t, "## Ship\n- [?] Pick a region @Builder\n  - ask: eu or us?\n- [ ] Write docs @Docs\n", builder("waiting"), docs())
		v.a.mu.Lock()
		v.a.thread(supTeam).Status = "needs_you" // what a lead may have set for the question
		v.a.saveThreads()
		v.a.mu.Unlock()
		typed0 := len(v.typed())
		v.now = v.now.Add(time.Hour)
		idleAt(t, v, v.now.Add(-time.Hour), builder("waiting"), docs())
		if w, _ := wakeNotes(v); w != 1 {
			bad = append(bad, fmt.Sprintf("%d wakes: the owner question on Builder's step stopped the wake for idle Docs work", w))
		}
		if len(v.typed()) != typed0 {
			bad = append(bad, "the wake typed into a pane")
		}
		v.a.mu.Lock()
		st := v.a.thread(supTeam).Status
		v.a.mu.Unlock()
		if st != "needs_you" {
			bad = append(bad, "the wake changed the lead's status to "+st)
		}
		scenario(t, "K8 needs_you is scoped: another idle step still wakes the lead; status kept", &bad)
	})
}
