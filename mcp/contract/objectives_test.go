package main

// Tests for objectives and evidence as the owner's feature page shows them, driven by what the client writes: the
// owner's FEATURE.md objective ids (D1, D2, ...), the step's canonical "- covers:" line (written by add_evidence), the
// evidence and assessment lines, and the done gate that refuses while an objective has no covering step. Nothing the
// agent writes may verify a step.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// vmObjFeature is a sample FEATURE.md with an objective text and two desired outcomes: D1 (with an owner-named proof)
// and D2.
const vmObjFeature = "# CSV export\n\n## Objective\nOwners export their data.\n\n## Desired outcomes\n- [ ] D1: CSV export works — done when: it opens in a spreadsheet\n  - proof: image of the opened file\n- [ ] D2: Docs say how — done when: README has a section\n"

// vmObjPage is the part of the owner's feature page these tests read: the objectives and the per-step evidence.
type vmObjPage struct {
	Objectives struct {
		Text  string
		Items []struct {
			ID, Text, DoneWhen, Proof string
			Steps, Missing            []string
			Met                       bool
		}
		Unknown []string
	}
	Evidence map[string]json.RawMessage
}

// vmObjPage loads the owner's feature page and returns its objectives and evidence.
func (w *writerEnv) vmObjPage(t *testing.T) vmObjPage {
	w.v.look(w.v.agent("working", w.v.now.Unix()))
	r := w.v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", w.v.e.authed(w.v.e.login(t)))
	raw, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var f struct{ Page vmObjPage }
	json.Unmarshal(raw, &f)
	return f.Page
}

// TestVMAgentObjectives checks that evidence, coverage and assessments written by the client land as typed fields on
// the page without verifying anything, and that accept and done are refused while an objective has no covering step.
func TestVMAgentObjectives(t *testing.T) {
	plan := "## Ship\n- [ ] Export CSV @Builder\n- [ ] Write the README @Docs\n"
	setup := func(t *testing.T) (*writerEnv, string, string, string) {
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		writeFile(t, d.Paths["FEATURE.md"], vmObjFeature) // the owner's FEATURE.md (agents get 403 on it)
		return w, d.Thread, d.Milestones[0].Steps[0].Key, d.Milestones[0].Steps[1].Key
	}

	t.Run("J1 the client's evidence, coverage and assessment land as the page's typed fields; nothing verified; author null", func(t *testing.T) {
		var bad []string
		w, thread, k1, k2 := setup(t)
		if out := w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'image', 'evidence/export.png', covers='D1', result='opens with 12 rows')", supTeam, thread, k1); !strings.HasPrefix(out, "OK") {
			t.Fatalf("add_evidence: %s", out)
		}
		if out := w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'file', 'evidence/export.csv', covers='D1, D9')", supTeam, thread, k1); !strings.HasPrefix(out, "OK") {
			t.Fatalf("second add_evidence: %s", out)
		}
		if out := w.py("m.add_assessment(sys.argv[1], sys.argv[2], sys.argv[3], 'pass', 'the screenshot shows 12 rows', 'evidence/export.png')", supTeam, thread, k1); !strings.HasPrefix(out, "OK") {
			t.Fatalf("add_assessment: %s", out)
		}
		planText, _ := os.ReadFile(w.v.planFile())
		if strings.Count(string(planText), "- covers:") != 1 || !strings.Contains(string(planText), "- covers: D1, D9") {
			bad = append(bad, "the step's covers line isn't one merged canonical line:\n"+string(planText))
		}
		p := w.vmObjPage(t)
		if p.Objectives.Text != "Owners export their data." || len(p.Objectives.Items) != 2 {
			t.Fatalf("page.objectives: %+v", p.Objectives)
		}
		d1, d2 := p.Objectives.Items[0], p.Objectives.Items[1]
		if d1.ID != "D1" || d1.DoneWhen != "it opens in a spreadsheet" || d1.Proof != "image of the opened file" || strings.Join(d1.Steps, ",") != k1 || d1.Met {
			bad = append(bad, fmt.Sprintf("D1: %+v (want covered by %s only, not met)", d1, k1))
		}
		if !strings.Contains(strings.Join(d1.Missing, ";"), k1+" isn't verified") {
			bad = append(bad, fmt.Sprintf("D1 missing: %v", d1.Missing))
		}
		if d2.ID != "D2" || len(d2.Steps) != 0 || d2.Met || !strings.Contains(strings.Join(d2.Missing, ";"), "no step covers it") {
			bad = append(bad, fmt.Sprintf("D2: %+v (want uncovered)", d2))
		}
		if strings.Join(p.Objectives.Unknown, ",") != "D9" {
			bad = append(bad, fmt.Sprintf("unknown covers: %v, want D9", p.Objectives.Unknown))
		}
		raw := string(p.Evidence[k1])
		var e struct {
			Required   *struct{ By, Text string }
			Check      json.RawMessage
			Attest     json.RawMessage
			Claim      string
			Items      []struct{ ID, Kind, Ref string }
			Assessment *struct {
				Verdict string
				Cites   []string
				Author  json.RawMessage
			}
		}
		json.Unmarshal(p.Evidence[k1], &e)
		if e.Required == nil || e.Required.By != "owner" || e.Required.Text != "image of the opened file" {
			bad = append(bad, "evidence.required: "+raw)
		}
		if e.Check != nil || e.Attest != nil {
			bad = append(bad, "evidence shows a check or attestation nobody made: "+raw)
		}
		// both evidence lines stay items (one per line, stable ids), each ref alone, none dropped or overwritten
		refs, ids := map[string]string{}, map[string]bool{}
		for _, x := range e.Items {
			refs[x.Ref], ids[x.ID] = x.Kind, true
		}
		if len(e.Items) != 2 || len(ids) != 2 || refs["evidence/export.png"] != "image" || refs["evidence/export.csv"] != "file" || strings.Contains(raw, "covers D") {
			bad = append(bad, "evidence.items (want both refs, image and file, each the ref alone, with distinct ids): "+raw)
		}
		if e.Assessment == nil || e.Assessment.Verdict != "pass" || strings.Join(e.Assessment.Cites, ",") != "evidence/export.png" || !strings.Contains(raw, `"author":null`) {
			bad = append(bad, "evidence.assessment (author must be null): "+raw)
		}
		if st := w.step(0); st.State == "done" || st.Label == "owner verified" || st.Label == "check ran (receipt)" {
			bad = append(bad, fmt.Sprintf("evidence + a pass assessment verified the step: %+v", st))
		}
		if _, ok := p.Evidence[k2]; ok {
			bad = append(bad, "evidence for a step with none: "+string(p.Evidence[k2]))
		}
		scenario(t, "J1 page.objectives (D-ids, done when, owner proof, covering steps from the canonical covers line, unmet with why, unknown ids) and page.evidence (owner requirement, claim, item ref alone, pass assessment with cites and author null, no check/attest); nothing verified", &bad)
	})

	t.Run("J2 accept and done are refused while an objective has no covering step, even with every step verified", func(t *testing.T) {
		var bad []string
		w, thread, k1, _ := setup(t)
		w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'image', 'evidence/export.png', covers='D1')", supTeam, thread, k1)
		cur, _ := os.ReadFile(w.v.planFile()) // the workers claim both done; the owner verifies each
		if c, out := w.fdoc(strings.ReplaceAll(string(cur), "- [ ]", "- [x]"), "put", "plan", "-", "--rev", w.rev(t, "plan")); c != 0 {
			t.Fatalf("precondition: claim done: %s", out)
		}
		for i := 0; i < 2; i++ {
			if c := w.ownerVerify(t, i); c != 200 {
				t.Fatalf("precondition: owner verify %d: %d", i, c)
			}
		}
		accept := func() (int, string) {
			d := w.v.e.s.featDossier(supTeam)
			c, out := w.owner(t, "/api/term/feature/accept", map[string]any{"session": supTeam, "rev": d.Revs["feature"]})
			e, _ := out["error"].(string)
			return c, e
		}
		if c, e := accept(); c != 409 || !strings.Contains(e, "D2") || !strings.Contains(e, "no step covering it") {
			bad = append(bad, fmt.Sprintf("the owner's accept with D2 uncovered: %d %q, want 409 naming D2", c, e))
		}
		if ok, out := w.vmDone(thread); ok || !strings.Contains(out, "D2") {
			bad = append(bad, "done with D2 uncovered: "+out)
		}
		p := w.vmObjPage(t)
		if len(p.Objectives.Items) != 2 || !p.Objectives.Items[0].Met || p.Objectives.Items[1].Met {
			bad = append(bad, fmt.Sprintf("with both steps verified D1 should be met and D2 not: %+v", p.Objectives.Items))
		}
		k2 := w.step(1).Key
		w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'file', 'README.md', covers='D2')", supTeam, thread, k2)
		if c, e := accept(); strings.Contains(e, "no step covering") {
			bad = append(bad, fmt.Sprintf("after a step covers D2 the accept still says uncovered: %d %q", c, e))
		}
		if p := w.vmObjPage(t); len(p.Objectives.Items) == 2 && strings.Join(p.Objectives.Items[1].Steps, ",") != k2 {
			bad = append(bad, fmt.Sprintf("D2 after the covers line: %+v", p.Objectives.Items[1]))
		}
		scenario(t, "J2 with every step verified: the owner's accept and done refused naming the uncovered D2; D1 met, D2 not; once a step covers D2 the gate stops naming it", &bad)
	})
}

// TestVMAgentObjectivesCoverageChange checks that changing what a step covers voids its attestation and emits a
// reviewable coverage event until the owner re-attests, and that malformed objective ids (D3a, d4) block acceptance
// like missing ones.
func TestVMAgentObjectivesCoverageChange(t *testing.T) {
	t.Run("J3 coverage edit voids the attestation, emits a coverage event; re-attest restores; malformed ids block", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [x] Export CSV @Builder\n  - covers: D1\n- [x] Write the README @Docs\n  - covers: D2\n")
		d := w.v.e.s.featDossier(supTeam)
		writeFile(t, d.Paths["FEATURE.md"], vmObjFeature)
		for i := 0; i < 2; i++ {
			if c := w.ownerVerify(t, i); c != 200 {
				t.Fatalf("precondition: verify %d: %d", i, c)
			}
		}
		k2 := w.step(1).Key
		if out := w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'file', 'README.md', covers='D1')", supTeam, d.Thread, k2); !strings.HasPrefix(out, "OK") {
			t.Fatalf("coverage edit: %s", out)
		}
		if st := w.step(1); st.Label == "owner verified" {
			bad = append(bad, "the owner's attestation still holds after the step's coverage changed (D2 -> D2, D1)")
		}
		var cov []string
		for _, e := range w.evRevs(t) {
			if e.Kind == "coverage" {
				cov = append(cov, e.ID)
			}
		}
		if len(cov) != 1 {
			bad = append(bad, fmt.Sprintf("coverage events after one coverage edit: %v, want 1", cov))
		} else if c, out := w.owner(t, "/api/term/feature/reviewed", map[string]any{"session": supTeam, "id": cov[0], "rev": w.evRevOf(t, cov[0])}); c != 200 {
			bad = append(bad, fmt.Sprintf("reviewing the coverage event: %d %v", c, out))
		}
		if ok, out := w.vmDone(d.Thread); ok {
			bad = append(bad, "done accepted with a voided attestation: "+out)
		}
		if c := w.ownerVerify(t, 1); c != 200 || w.step(1).Label != "owner verified" {
			bad = append(bad, fmt.Sprintf("re-attesting the step: %d %+v", c, w.step(1)))
		}
		p := w.vmObjPage(t)
		if len(p.Objectives.Items) != 2 || !p.Objectives.Items[0].Met || !p.Objectives.Items[1].Met {
			bad = append(bad, fmt.Sprintf("after re-attesting both objectives should be met: %+v", p.Objectives.Items))
		}
		writeFile(t, d.Paths["FEATURE.md"], vmObjFeature+"- [ ] D3a: malformed id\n- [ ] d4: lower-case id\n")
		dd := w.v.e.s.featDossier(supTeam)
		c, out := w.owner(t, "/api/term/feature/accept", map[string]any{"session": supTeam, "rev": dd.Revs["feature"]})
		if e := fmt.Sprint(out["error"]); c != 409 || !(strings.Contains(e, "D3a") || strings.Contains(e, "d4")) {
			bad = append(bad, fmt.Sprintf("accept with malformed objective ids D3a/d4: %d %q, want 409 naming them", c, e))
		}
		scenario(t, "J3 coverage edit: attestation void, one reviewable coverage event, done refused, re-attest restores both objectives; malformed D3a/d4 block accept", &bad)
	})
}
