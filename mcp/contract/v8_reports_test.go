package main

// Tests for the structured reports a lead makes through vm_mcp, all on the existing record (no extra panel API):
// ask_owner puts a question to the owner as a [?] step with an ask: line (under the plan revision and the thread
// guard; the thread shows needs_you), which the owner's existing answer route answers for that step only; decision and
// action reports carry their step and plan revision in LOG.md; evidence and assessments go under the step through the
// plan writer and never verify it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVMAgentV8Reports runs the ask_owner, decision/action report and evidence/assessment checks as subtests.
func TestVMAgentV8Reports(t *testing.T) {
	plan := "## Ship\n- [ ] Pick region @Builder\n  - kind: mutation\n- [ ] Write docs @Docs\n"
	t.Run("X1 ask_owner: a [?] step with ask:, needs_you, answered by the owner for that step only", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		thread, key, other := d.Thread, d.Milestones[0].Steps[0].Key, d.Milestones[0].Steps[1]
		if out := w.py("m.ask_owner(sys.argv[1], 'someone-else', sys.argv[2], 'eu or us?')", supTeam, key); !strings.HasPrefix(out, "REFUSED") {
			bad = append(bad, "another thread's ask: "+out)
		}
		if b, _ := os.ReadFile(w.v.planFile()); string(b) != plan {
			bad = append(bad, "a refused ask wrote PLAN.md")
		}
		if out := w.py("m.ask_owner(sys.argv[1], sys.argv[2], sys.argv[3], 'eu or us?', options='eu | us', why='DNS waits on it')", supTeam, thread, key); !strings.HasPrefix(out, "OK asked") {
			t.Fatalf("ask_owner: %s", out)
		}
		d = w.v.e.s.featDossier(supTeam)
		st := d.Milestones[0].Steps[0]
		if st.State != "asking" || st.Qid == "" || !strings.Contains(st.Ask, "eu or us?") {
			bad = append(bad, fmt.Sprintf("the step isn't an owner question: %+v", st))
		}
		if st.Kind != "mutation" || st.Owner != "Builder" {
			bad = append(bad, fmt.Sprintf("the ask lost the step's sub-lines or owner: %+v", st))
		}
		if o := d.Milestones[0].Steps[1]; o.State != other.State || o.Key != other.Key {
			bad = append(bad, "the ask changed another step")
		}
		w.v.a.mu.Lock()
		status := w.v.a.thread(thread).Status
		w.v.a.mu.Unlock()
		if status != "working" { // scoped: only the asked step waits; the lead's status is the lead's
			bad = append(bad, "a scoped question changed the thread status to "+status)
		}
		body, _ := json.Marshal(map[string]any{"session": supTeam, "key": st.Key, "qid": st.Qid, "rev": d.Rev, "text": "eu", "resume": false})
		if r := w.v.e.do(t, "POST", "/api/term/feature/answer", string(body), w.v.e.authed(w.v.e.login(t))); r.StatusCode != 200 {
			bad = append(bad, fmt.Sprintf("the owner's answer: %d", r.StatusCode))
		}
		d = w.v.e.s.featDossier(supTeam)
		if a := d.Milestones[0].Steps[0]; !strings.Contains(a.Answer, "eu") {
			bad = append(bad, fmt.Sprintf("the answer isn't on the step: %+v", a))
		}
		if a := d.Milestones[0].Steps[0]; a.Approval.Check || a.Approval.Kind {
			bad = append(bad, "the answer approved the step's check or kind")
		}
		if o := d.Milestones[0].Steps[1]; o.Answer != "" {
			bad = append(bad, "the answer reached another step")
		}
		scenario(t, "X1 ask_owner: scoped owner question (thread status kept), guarded write, answered for that step only, nothing approved", &bad)
	})

	t.Run("X2 decision/action reports carry their step and plan revision", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		key := d.Milestones[0].Steps[1].Key
		for _, k := range []string{"decision", "action"} {
			if out := w.py("m.report_feature_progress(sys.argv[1], sys.argv[2], 'docs before the region', step=sys.argv[3], thread=sys.argv[4], rev=sys.argv[5], considered=['docs first', 'region first'], chose='docs first', by='Docs')", supTeam, k, key, d.Thread, d.Rev); !strings.HasPrefix(out, "OK logged") {
				bad = append(bad, k+": "+out)
			}
		}
		lb, _ := os.ReadFile(filepath.Join(filepath.Dir(w.v.planFile()), "LOG.md"))
		tail := " | considered: docs first; region first | chose: docs first | step: " + key + " | rev: " + d.Rev + " | by: @Docs"
		for _, want := range []string{"DECISION: docs before the region" + tail, "ACTION: docs before the region" + tail} {
			if !strings.Contains(string(lb), want) {
				bad = append(bad, "LOG.md lacks "+want)
			}
		}
		if w.v.e.s.featDossier(supTeam).Milestones[0].Steps[1].State == "done" {
			bad = append(bad, "a report verified a step")
		}
		scenario(t, "X2 public decision/action log lines with step key and plan rev; nothing verified", &bad)
	})

	t.Run("X3 evidence and an assessment go under the step through the writer; neither verifies the step or accepts outcomes", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [x] Export CSV @Builder\n  - kind: build\n- [ ] Other\n")
		dir := filepath.Dir(w.v.planFile())
		writeFile(t, filepath.Join(dir, "FEATURE.md"), "# Desired outcome\n- [ ] D1: CSV export works — done when: a CSV opens\n")
		d := w.v.e.s.featDossier(supTeam)
		key, thread := d.Milestones[0].Steps[0].Key, d.Thread
		if out := w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'image', 'evidence/export.png', covers='D1', result='12 rows')", supTeam, thread, key); !strings.HasPrefix(out, "OK evidence added") {
			bad = append(bad, "add_evidence: "+out)
		}
		if out := w.py("m.add_assessment(sys.argv[1], sys.argv[2], sys.argv[3], 'pass', 'rows match', 'evidence/export.png')", supTeam, thread, key); !strings.HasPrefix(out, "OK assessment recorded") {
			bad = append(bad, "add_assessment: "+out)
		}
		b, _ := os.ReadFile(w.v.planFile())
		want := "## Ship\n- [x] Export CSV @Builder\n  - covers: D1\n  - kind: build\n  - evidence: image evidence/export.png\n  - result: 12 rows\n  - assessment: pass — rows match — cites evidence/export.png\n- [ ] Other\n"
		if string(b) != want {
			bad = append(bad, fmt.Sprintf("PLAN.md: %q", b))
		}
		d = w.v.e.s.featDossier(supTeam)
		if st := d.Milestones[0].Steps[0]; st.State == "done" || st.Label == "owner verified" {
			bad = append(bad, fmt.Sprintf("evidence + a pass assessment verified the step: %+v", st))
		}
		if d.Accepted {
			bad = append(bad, "an assessment accepted the outcomes")
		}
		if ok, _ := w.vmDone("demo-feature"); ok {
			bad = append(bad, "done accepted on a model assessment")
		}
		if out := w.py("m.add_assessment(sys.argv[1], 'someone-else', sys.argv[2], 'pass', 'x', 'y')", supTeam, key); !strings.HasPrefix(out, "REFUSED") {
			bad = append(bad, "another thread's assessment: "+out)
		}
		scenario(t, "X3 evidence + assessment via the writer; claimed/assessed only, never verified or accepted", &bad)
	})
}
