package main

// Regression tests for the panel's feature supervisor and its owner/agent authority split. Each case asserts the
// correct behaviour (for example: an undelivered event for a lead is never dropped, and an agent can never approve or
// verify its own work) and logs "SCENARIO ok|FAILED: <scenario name>". The file lives in vm-agent (mcp/contract/) and
// run.sh runs it inside a copy of a control-panel checkout.

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

// TestVMAgentReviewRegressions runs the supervisor and authority regressions as subtests: event delivery to a lead
// across a Hermes outage and a restart, stale or replaced owner questions, refused "done" updates, held mutations,
// fabricated evidence, manager attachment, per-feature isolation of holds and questions, and the rule that only the
// owner can verify a step.
func TestVMAgentReviewRegressions(t *testing.T) {
	t.Run("R1 a lead's Hermes answering 503 keeps the event pending across a restart", func(t *testing.T) {
		var bad []string
		v := newSupEnv(t, "")
		good := v.a.base
		down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "offline", 503) }))
		defer down.Close()
		v.a.base = down.URL
		v.a.mu.Lock()
		v.a.thread(supTeam).TeamSeen = v.e.s.notes.lastID()
		v.a.mu.Unlock()
		v.e.s.notes.push("feature", "warn", "critical blocker", "must deliver", "thread:"+supTeam)
		v.e.s.notes.mu.Lock() // a supervisor event: news for the lead only with its dedupe key (as raise sets it)
		v.e.s.notes.Notes[len(v.e.s.notes.Notes)-1].Ref = "critical"
		v.e.s.notes.saveLocked()
		v.e.s.notes.mu.Unlock()
		id := v.e.s.notes.lastID()
		v.e.s.teamDeliver(v.now)
		waitIdle(t, v.a, supTeam)
		v.a.mu.Lock()
		v.a.saveThreads()
		v.a.threads = nil
		v.a.loadThreads() // restart: what survives is on disk
		seen := v.a.thread(supTeam).TeamSeen
		v.a.mu.Unlock()
		if seen >= id {
			bad = append(bad, "the cursor passed the undelivered note after a 503 (and a restart)")
		}
		v.a.base = good
		teamPending.Lock()
		teamPending.m = map[string]teamHand{}
		teamPending.Unlock()
		v.e.s.notes = newNotifier(v.e.s.notes.file)
		v.e.s.teamDeliver(v.now)
		if !strings.Contains(strings.Join(v.lead(t), "\n"), "critical blocker") {
			bad = append(bad, "after Hermes came back the blocker never reached the lead")
		}
		scenario(t, "R1 Hermes 503: the worker event stays pending across a restart and is delivered later", &bad)
	})

	t.Run("R2 an answer to a replaced question is refused", func(t *testing.T) {
		var bad []string
		v := newSupEnv(t, "## Work\n- [?] Choose target @Builder\n  - ask: Use sandbox?\n")
		d := v.e.s.featDossier(supTeam) // what the owner's page showed
		key, rev := d.Milestones[0].Steps[0].Key, d.Rev
		changed := "## Work\n- [?] Choose target @Builder\n  - ask: Use production?\n"
		writeFile(t, v.planFile(), changed)
		body, _ := json.Marshal(map[string]string{"session": supTeam, "key": key, "rev": rev, "text": "Yes"})
		if c, _ := call(v.e.s.termFeatureAnswer, string(body)); c == 200 {
			bad = append(bad, "a Yes to 'Use sandbox?' was accepted after the question became 'Use production?'")
		}
		if b, _ := os.ReadFile(v.planFile()); string(b) != changed {
			bad = append(bad, "PLAN.md changed: "+string(b))
		}
		scenario(t, "R2 stale question/revision: the answer fails and nothing changes", &bad)
	})

	t.Run("R3 done together with a todo plan is refused", func(t *testing.T) {
		var bad []string
		plan := "## Work\n- [x] Check output\n  - evidence: output.txt\n  - result: ok\n"
		v := newSupEnv(t, plan)
		writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "output.txt"), "ok\n")
		if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done","plan":"- [ ] Check output"}`); c == 200 {
			bad = append(bad, "status done committed together with a todo step")
		}
		v.a.mu.Lock()
		st := v.a.thread(supTeam).Status
		v.a.mu.Unlock()
		if st == "done" {
			bad = append(bad, "the thread is done")
		}
		if b, _ := os.ReadFile(v.planFile()); string(b) != plan {
			bad = append(bad, "PLAN.md changed by a refused update")
		}
		scenario(t, "R3 done + todo in one update fails, nothing committed", &bad)
	})

	t.Run("R4 a conversation that also owns an uncertain mutation is not resumed", func(t *testing.T) {
		var bad []string
		v := newSupEnv(t, "## Work\n- [~] Read logs @Builder\n  - kind: read\n- [~] Change DNS @Builder\n  - kind: mutation\n")
		v.look(v.agent("working", v.now.Unix()))
		v.look()
		v.look()
		if typed := v.typed(); len(typed) > 0 {
			bad = append(bad, "resumed it: "+strings.Join(typed, " | "))
		}
		if v.e.s.featDossier(supTeam).Milestones[0].Steps[1].Blocked == "" {
			bad = append(bad, "the mutation isn't held")
		}
		waitIdle(t, v.a, supTeam)
		scenario(t, "R4 mixed safe + uncertain mutation in one conversation: no resume, mutation held", &bad)
	})

	t.Run("R5 fabricated initial evidence never verifies", func(t *testing.T) {
		var bad []string
		v := newSupEnv(t, "## Ship\n- [x] Production is healthy @Builder\n  - evidence: bogus.txt\n  - result: success\n")
		writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "bogus.txt"), "success\n")
		if s := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; s.State == "done" {
			bad = append(bad, "the step reads done on an agent-written file")
		}
		if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c == 200 {
			bad = append(bad, "status done accepted")
		}
		scenario(t, "R5 fabricated evidence: the step stays claimed, done refused", &bad)
	})

	t.Run("R6 attaching a manager that has its own record is refused, not shadowed", func(t *testing.T) {
		var bad []string
		v := newSupEnv(t, "")
		const sess = "Owner-Six"
		os.WriteFile(filepath.Join(v.e.s.sys.sockDir, sess), nil, 0o600)
		own := filepath.Join(featDir(v.e.s.cfg.home, sess), "PLAN.md")
		writeFile(t, own, "## Owner\n- [ ] Owner step\n")
		writeFile(t, filepath.Join(featDir(v.e.s.cfg.home, sess), "FEATURE.md"), "Owner's outcome.\n")
		v.a.mu.Lock()
		m := v.a.newThread("Manager with a record", "")
		v.a.mu.Unlock()
		writeFile(t, filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", m.ID, "PLAN.md"), "## Manager\n- [ ] Manager step\n")
		if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"`+m.ID+`","team":"`+sess+`"}`); c == 200 {
			d := v.e.s.featDossier(sess)
			bad = append(bad, "bound although both have a PLAN.md; the dossier reads "+d.Paths["PLAN.md"]+" with conflicts "+strings.Join(d.Conflicts, " "))
		}
		// a manager without its own record attaches; every document, new ones too, has the owner's folder as its one place
		v.a.mu.Lock()
		m2 := v.a.newThread("Manager without one", "")
		v.a.mu.Unlock()
		const sess2 = "Owner-Six-B"
		os.WriteFile(filepath.Join(v.e.s.sys.sockDir, sess2), nil, 0o600)
		writeFile(t, filepath.Join(featDir(v.e.s.cfg.home, sess2), "PLAN.md"), "## Owner\n- [ ] Owner step\n")
		if c, out := call(v.e.s.agentThreadUpdate, `{"id":"`+m2.ID+`","team":"`+sess2+`"}`); c != 200 {
			bad = append(bad, "a plain attachment failed: "+out["error"].(string))
		}
		for _, f := range []string{"TESTS.md", "LOG.md", "FEATURE.md"} {
			if p := v.e.s.featPath(sess2, f); filepath.Dir(p) != featDir(v.e.s.cfg.home, sess2) {
				bad = append(bad, f+" would go to "+p+", not the feature's folder (a split dossier)")
			}
		}
		scenario(t, "R6 canonical attachment: a conflicting manager record is refused, documents stay in one folder", &bad)
	})

	t.Run("R7 identical steps in two features don't share holds or questions", func(t *testing.T) {
		var bad []string
		plan := "## Deploy\n- [~] Apply change @Builder\n  - kind: mutation\n- [?] Pick a window @Builder\n  - ask: Tonight?\n"
		v := newSupEnv(t, plan)
		const other = "other-feature"
		v.a.mu.Lock()
		o := v.a.newThread("Other feature", "")
		o.Status, o.Created = "working", 1
		v.a.saveThreads()
		v.a.mu.Unlock()
		os.WriteFile(filepath.Join(v.e.s.sys.sockDir, other), nil, 0o600)
		writeFile(t, filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", other, "PLAN.md"), plan)
		look := func(withDemo bool) {
			v.now = v.now.Add(31e9)
			ss := []TSession{{Name: other, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: []TPane{v.agent("working", v.now.Unix())}}}}}
			if withDemo {
				ss = append(ss, TSession{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: []TPane{v.agent("working", v.now.Unix())}}}})
			}
			v.e.s.observeTeams(v.now, Terminals{Sessions: ss}, CI{}, Resources{})
		}
		look(true)
		look(false)
		look(false) // demo-feature's agent exited mid-mutation
		if b := v.e.s.featDossier(supTeam).Milestones[0].Steps[0].Blocked; b == "" {
			bad = append(bad, "the exited feature's mutation isn't held")
		}
		if b := v.e.s.featDossier(other).Milestones[0].Steps[0].Blocked; b != "" {
			bad = append(bad, "the other feature's identical step is held too: "+b)
		}
		asked := map[string]bool{}
		for _, n := range v.notes("") {
			if strings.Contains(n.Title, "Tonight?") || strings.Contains(n.Body, "Tonight?") {
				asked[n.Go] = true
			}
		}
		if !asked["thread:"+supTeam] || !asked["thread:"+other] {
			bad = append(bad, "the same question in two features was raised for only one: "+strings.Join(keys(asked), ","))
		}
		waitIdle(t, v.a, supTeam)
		scenario(t, "R7 identical cross-feature steps: holds and questions stay per feature", &bad)
	})

	// Verification rules: worker files and prose only ever leave a step "claimed"; only an explicit owner approval of
	// the exact check (or an owner attestation) can make it verified. These two cases are the negatives: things that
	// must not verify a step.
	t.Run("V1 the owner saving PLAN.md is not an approval", func(t *testing.T) {
		var bad []string
		v := newSupEnv(t, "")
		h := v.e.authed(v.e.login(t))
		plan := "## Ship\n- [x] Tests pass @Tester\n  - check: go test ./...\n  - evidence: test.log\n  - result: `ok`\n"
		body, _ := json.Marshal(map[string]string{"session": supTeam, "doc": "plan", "text": plan, "rev": v.e.s.featDossier(supTeam).Rev})
		if r := v.e.do(t, "POST", "/api/term/feature/doc", string(body), h); r.StatusCode != 200 {
			bad = append(bad, "the owner's page save failed")
		}
		writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "test.log"), "ok  \tcontrol-panel\n")
		if s := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; s.State == "done" {
			bad = append(bad, "a check line in an owner-saved PLAN.md, plus a worker file, verified the step")
		}
		scenario(t, "V1 implicit approval (saving PLAN.md) doesn't verify", &bad)
	})

	t.Run("V2 an agent can't approve or attest", func(t *testing.T) {
		var bad []string
		v := newSupEnv(t, "## Ship\n- [ ] Tests pass @Tester\n")
		writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "test.log"), "ok  \tcontrol-panel\n")
		for _, lines := range []string{"  - approved: owner\n", "  - verified-by: owner\n", "  - attested: owner\n", "  - owner-verified: yes\n"} {
			os.WriteFile(v.planFile(), []byte("## Ship\n- [x] Tests pass @Tester\n  - evidence: test.log\n  - result: `ok`\n"+lines), 0o600)
			if s := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; s.State == "done" {
				bad = append(bad, "agent-written "+strings.TrimSpace(lines)+" verified the step")
			}
		}
		if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c == 200 {
			bad = append(bad, "the agent API accepted done")
		}
		scenario(t, "V2 agent-written approval/attestation text doesn't verify; the agent API can't complete", &bad)
	})
}

// TestVMAgentOwnerRoutesNotAgentReachable checks that the owner-only feature routes (approve, verify, release, answer,
// accept) are never reachable with the agent bearer token: they are absent from the agent API and the page server
// refuses them without the owner's session. Whatever the request body looks like, none may answer below 400, and
// PLAN.md must stay unchanged.
func TestVMAgentOwnerRoutesNotAgentReachable(t *testing.T) {
	var bad []string
	v := newSupEnv(t, "## Work\n- [?] Choose @Builder\n  - ask: Sandbox?\n")
	agent := httptest.NewServer(v.e.s.agentHandler("agent-token"))
	defer agent.Close()
	body := `{"session":"demo-feature","key":"k","qid":"q","rev":"r","text":"Yes","resume":true,"items":[{"key":"k","check":"true","cwd":"/"}],"tab":"Builder"}`
	plan, _ := os.ReadFile(v.planFile())
	for _, r := range []string{"approve", "verify", "release", "answer", "accept"} {
		for _, base := range []string{agent.URL + "/api/term/feature/" + r, agent.URL + "/agent/feature/" + r, v.e.srv.URL + "/api/term/feature/" + r} {
			req, _ := http.NewRequest("POST", base, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer agent-token")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				bad = append(bad, base+": "+err.Error())
				continue
			}
			resp.Body.Close()
			if resp.StatusCode < 400 {
				bad = append(bad, fmt.Sprintf("%s answered %d to the agent bearer", base, resp.StatusCode))
			}
		}
	}
	if b, _ := os.ReadFile(v.planFile()); string(b) != string(plan) {
		bad = append(bad, "PLAN.md changed")
	}
	scenario(t, "W1 owner-only routes (approve, verify, release, answer, accept) refuse the agent bearer on both servers", &bad)
}
