package main

// Tests for how a step's "- proof:" line is met. A run proof's backtick literals are a mechanical predicate over THIS
// step's latest owner-approved receipt (the recorded run of its check), bound to the criterion as it stands now;
// nothing else (a file, a seal, a provenance, an assessment by anyone) meets a proof, and a non-run proof is met only
// by the owner's attestation. A literal holding is a predicate, not objective truth: the page says "mechanical check",
// and the owner still accepts the outcomes. The checks are run for real and recorded in the agent's transcript the way
// Claude Code writes it (runReceipt). The file also covers a run line and a file line on one step being both kept, and
// the guard against secret files that appear where evidence was missing.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// r2Crit is the "criterion" part of a step's page evidence: whether the proof is met, how, and what is missing.
type r2Crit struct {
	Met     *bool
	How     string
	Missing []string
}

// r2Evidence returns the criterion state, and the raw evidence JSON, that the owner's page shows for the step with the
// given key.
func (w *writerEnv) r2Evidence(t *testing.T, key string) (r2Crit, string) {
	r := w.v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", w.v.e.authed(w.v.e.login(t)))
	var f struct {
		Page struct {
			Evidence map[string]json.RawMessage
		}
	}
	json.NewDecoder(r.Body).Decode(&f)
	r.Body.Close()
	var e struct{ Criterion *r2Crit }
	json.Unmarshal(f.Page.Evidence[key], &e)
	if e.Criterion == nil {
		return r2Crit{}, string(f.Page.Evidence[key])
	}
	return *e.Criterion, string(f.Page.Evidence[key])
}

// runReceipt has the owner approve step i's check (when approve is true), runs the served runAs for real in the step's
// folder, writes the run to the agent's transcript as Claude Code writes it, and then does one supervisor look. It
// returns the step's attempt id.
func (w *writerEnv) runReceipt(t *testing.T, i int, approve bool) string {
	d := w.v.e.s.featDossier(supTeam)
	st := d.Milestones[0].Steps[i]
	if approve {
		ab, _ := json.Marshal(map[string]any{"session": supTeam, "items": []map[string]string{{"key": st.Key, "check": st.Check, "cwd": st.Cwd}}})
		if r := w.v.e.do(t, "POST", "/api/term/feature/approve", string(ab), w.v.e.authed(w.v.e.login(t))); r.StatusCode != 200 {
			t.Fatalf("approve: %d", r.StatusCode)
		}
		st = w.v.e.s.featDossier(supTeam).Milestones[0].Steps[i]
	}
	if st.RunAs == "" {
		t.Fatalf("no runAs for %s", st.Key)
	}
	out, err := exec.Command("bash", "-c", st.RunAs).CombinedOutput()
	id := fmt.Sprintf("toolu_run_%s_%d", st.Key, time.Now().UnixNano())
	at := time.Now().Add(time.Second)
	use, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": at.Add(-time.Second).Format(time.RFC3339Nano), "sessionId": "aaaa-bbbb-1",
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": st.RunAs}}}}})
	res := map[string]any{"type": "tool_result", "tool_use_id": id, "content": string(out)}
	if err != nil {
		res["is_error"] = true
	}
	rec, _ := json.Marshal(map[string]any{"type": "user", "timestamp": at.Format(time.RFC3339Nano), "sessionId": "aaaa-bbbb-1",
		"message": map[string]any{"content": []any{res}}, "toolUseResult": map[string]any{"stdout": string(out), "stderr": "", "interrupted": false}})
	f, _ := os.OpenFile(filepath.Join(w.v.cwd, "transcript.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.Write(append(append(use, '\n'), append(rec, '\n')...))
	f.Close()
	w.v.look(w.v.agent("working", w.v.now.Unix()))
	return st.Attempt
}

// TestVMAgentR2Proof checks when a proof is met: a run proof only by its literals appearing in this step's own latest
// receipt output, a changed criterion or a later failing run un-meets it, a non-run proof only by the owner's
// attestation, and a step with no proof line keeps the plain receipt rule.
func TestVMAgentR2Proof(t *testing.T) {
	// mk builds a feature with a step that has an owner-approvable check (printing what it found) in its own folder,
	// plus a proof line from the plan when proof is not empty
	mk := func(t *testing.T, check, proof string) (*writerEnv, string, string) {
		cwd := t.TempDir()
		plan := fmt.Sprintf("## Ship\n- [x] Export CSV @Builder\n  - covers: D1\n  - check: %s\n  - cwd: %s\n", check, cwd)
		if proof != "" {
			plan += "  - proof: " + proof + "\n"
		}
		w := newWriterEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		writeFile(t, d.Paths["FEATURE.md"], "# Export\n\n## Desired outcomes\n- [ ] D1: CSV export works\n")
		w.v.e.s.cfg.pinFile = filepath.Join(w.v.e.s.cfg.home, ".config", "control-panel", "pin")
		w.v.a.mu.Lock()
		w.v.a.mu.Unlock() //nolint:staticcheck
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		return w, d.Thread, d.Milestones[0].Steps[0].Key
	}
	met := func(c r2Crit) bool { return c.Met != nil && *c.Met }
	addEv := func(t *testing.T, w *writerEnv, thread, key, kind, ref string) {
		if out := w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5])", supTeam, thread, key, kind, ref); !strings.HasPrefix(out, "OK") {
			t.Fatalf("add_evidence: %s", out)
		}
	}

	t.Run("C3 run proof: the literal in THIS receipt's output meets it as a mechanical check; absent, or only in the command text, it doesn't", func(t *testing.T) {
		var bad []string
		w, thread, key := mk(t, "printf 'exported 12 rows\\n'", "run — exit 0 and output names `12 rows`")
		addEv(t, w, thread, key, "run", w.runReceipt(t, 0, true))
		if c, raw := w.r2Evidence(t, key); !met(c) || c.How != "mechanical check" {
			bad = append(bad, "the literal in the receipt's output: "+raw)
		}
		w2, thread2, key2 := mk(t, "printf 'exported 3 rows\\n'", "run — exit 0 and output names `12 rows`")
		addEv(t, w2, thread2, key2, "run", w2.runReceipt(t, 0, true))
		if c, raw := w2.r2Evidence(t, key2); met(c) || len(c.Missing) != 1 || !strings.Contains(c.Missing[0], "12 rows") || w2.step(0).State == "done" {
			bad = append(bad, "the literal absent from the output: "+raw)
		}
		w3, thread3, key3 := mk(t, "true # 12 rows", "run — exit 0 and output names `12 rows`")
		addEv(t, w3, thread3, key3, "run", w3.runReceipt(t, 0, true))
		if c, raw := w3.r2Evidence(t, key3); met(c) {
			bad = append(bad, "the literal only in the check's command text, not its output: "+raw)
		}
		scenario(t, "R2-C3 run proof literal: met (mechanical check) only from this receipt's output; absent -> unmet with the missing clause; command text doesn't count", &bad)
	})

	t.Run("C3b the criterion edited after the receipt: the old receipt doesn't meet the new criterion", func(t *testing.T) {
		var bad []string
		w, thread, key := mk(t, "printf 'exported 12 rows\\n'", "run — exit 0 and output names `12 rows`")
		addEv(t, w, thread, key, "run", w.runReceipt(t, 0, true))
		cur, _ := os.ReadFile(w.v.planFile())
		if c, out := w.fdoc(strings.Replace(string(cur), "`12 rows`", "`99 rows`", 1), "put", "plan", "-", "--rev", w.rev(t, "plan")); c != 0 {
			t.Fatalf("edit the proof: %s", out)
		}
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		if c, raw := w.r2Evidence(t, key); met(c) || w.step(0).Label == "check ran (receipt)" {
			bad = append(bad, "after the criterion became `99 rows`, the receipt that showed `12 rows` still meets it: "+raw)
		}
		scenario(t, "R2-C3b criterion/receipt binding: a criterion changed after the run isn't met by that run", &bad)
	})

	t.Run("C3c target: another step's receipt never meets this step's run proof", func(t *testing.T) {
		var bad []string
		cwd := t.TempDir()
		w := newWriterEnv(t, fmt.Sprintf("## Ship\n- [x] Export CSV @Builder\n  - covers: D1\n  - check: printf '12 rows\\n'\n  - cwd: %s\n- [x] Report @Builder\n  - covers: D1\n  - proof: run — exit 0 and output names `12 rows`\n", cwd))
		d := w.v.e.s.featDossier(supTeam)
		writeFile(t, d.Paths["FEATURE.md"], "# Export\n\n## Desired outcomes\n- [ ] D1: CSV export works\n")
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		at := w.runReceipt(t, 0, true)
		k2 := d.Milestones[0].Steps[1].Key
		w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'run', sys.argv[4])", supTeam, d.Thread, k2, at) // cites step 1's attempt
		if c, raw := w.r2Evidence(t, k2); met(c) || w.step(1).Label == "check ran (receipt)" {
			bad = append(bad, "step 2's proof met by step 1's receipt: "+raw)
		}
		scenario(t, "R2-C3c a receipt is the step's own: another step's attempt cited as evidence doesn't meet this proof", &bad)
	})

	t.Run("C5 a later failing run beats an earlier pass", func(t *testing.T) {
		var bad []string
		cwd := t.TempDir()
		flag := filepath.Join(cwd, "fail")
		w, thread, key := mk(t, fmt.Sprintf("printf '12 rows\\n'; test ! -e %s", flag), "run — exit 0 and output names `12 rows`")
		addEv(t, w, thread, key, "run", w.runReceipt(t, 0, true))
		writeFile(t, flag, "x")
		w.runReceipt(t, 0, false)
		if c, raw := w.r2Evidence(t, key); met(c) {
			bad = append(bad, "met after a later failing run: "+raw)
		}
		scenario(t, "R2-C5 contradictory proof: the latest run failed -> not met", &bad)
	})

	t.Run("C1/C2/C4 a non-run proof: sealed file + provenance + pass assessments (observed other, author, lead) never meet it; only the owner", func(t *testing.T) {
		var bad []string
		w, thread, key := mk(t, "true", "file — the CSV with 12 rows")
		dir := filepath.Dir(w.v.planFile())
		p := filepath.Join(dir, "export.csv")
		writeFile(t, p, "12 rows\n")
		addEv(t, w, thread, key, "file", "export.csv")
		w.py("m.add_assessment(sys.argv[1], sys.argv[2], sys.argv[3], 'pass', 'the file has 12 rows', 'export.csv')", supTeam, thread, key)
		c, raw := w.r2Evidence(t, key)
		if met(c) || c.How != "needs your attestation" || w.step(0).Label == "check ran (receipt)" {
			bad = append(bad, "a non-run proof with a sealed file and a pass assessment: "+raw)
		}
		if code := w.ownerVerify(t, 0); code != 200 {
			bad = append(bad, fmt.Sprintf("the owner's attestation: %d", code))
		} else if c, raw := w.r2Evidence(t, key); !met(c) || c.How != "owner attested" {
			bad = append(bad, "after the owner's attestation: "+raw)
		}
		scenario(t, "R2-C1/C2/C4 non-run proof: file + seal + assessments -> needs your attestation; the owner's attestation meets it", &bad)
	})

	t.Run("C2b an assessment by another observed pane session (feature-doc assess, its sid) changes nothing", func(t *testing.T) {
		var bad []string
		w, thread, key := mk(t, "true", "file — the CSV with 12 rows")
		writeFile(t, filepath.Join(filepath.Dir(w.v.planFile()), "export.csv"), "12 rows\n")
		addEv(t, w, thread, key, "file", "export.csv")
		w.setTabs(map[string]string{"Builder": "sid-b1", "Reviewer": "sid-r1"}) // the reviewer is observed, on another tab
		c := exec.Command("python3", filepath.Join(w.vm, "bin", "feature-doc"), "--session", supTeam, "assess", key, "pass", "--reasons", "12 rows as required", "--cites", "export.csv")
		c.Env = append(os.Environ(), "PANEL_AGENT_URL="+w.url, "PANEL_AGENT_TOKEN_FILE="+w.tok, "CLAUDE_CODE_SESSION_ID=sid-r1")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("feature-doc assess: %v %s", err, out)
		}
		cr, raw := w.r2Evidence(t, key)
		if met(cr) || cr.How != "needs your attestation" || w.step(0).Label == "owner verified" || w.step(0).Label == "check ran (receipt)" {
			bad = append(bad, "an observed other session's pass assessment changed the proof's state: "+raw)
		}
		t.Logf("R2-C2b observation: the page's assessment for an observed other session: %s", raw)
		scenario(t, "R2-C2b an assessment by another observed session (sid claim via feature-doc): a judgement, no state change", &bad)
	})

	t.Run("C6 an owner-named proof isn't met by a matching model run proof", func(t *testing.T) {
		var bad []string
		w, thread, key := mk(t, "printf '12 rows\\n'", "run — exit 0 and output names `12 rows`")
		d := w.v.e.s.featDossier(supTeam)
		writeFile(t, d.Paths["FEATURE.md"], "# Export\n\n## Desired outcomes\n- [ ] D1: CSV export works\n  - proof: a screenshot of the opened CSV\n")
		addEv(t, w, thread, key, "run", w.runReceipt(t, 0, true))
		if c, raw := w.r2Evidence(t, key); met(c) || c.How != "needs your attestation" {
			bad = append(bad, "an owner-named proof met by the model's run proof: "+raw)
		}
		scenario(t, "R2-C6 owner proof: a model run proof with a matching receipt doesn't meet it", &bad)
	})

	t.Run("C8 no proof line: the existing receipt rule, nothing new required", func(t *testing.T) {
		var bad []string
		w, thread, key := mk(t, "printf 'ok\\n'", "")
		addEv(t, w, thread, key, "run", w.runReceipt(t, 0, true))
		if st := w.step(0); st.Label != "check ran (receipt)" {
			bad = append(bad, fmt.Sprintf("no proof line: %+v (want verified by its receipt, as it was without proof lines)", st))
		}
		scenario(t, "R2-C8 no proof line: the receipt verifies as before; no assessor or new gate", &bad)
	})

	t.Run("N11 a run line and a file line on one step are both kept, in either order", func(t *testing.T) {
		var bad []string
		for _, runFirst := range []bool{true, false} {
			w, thread, key := mk(t, "printf 'ok\\n'", "")
			writeFile(t, filepath.Join(filepath.Dir(w.v.planFile()), "out.csv"), "a\n")
			if !runFirst {
				addEv(t, w, thread, key, "file", "out.csv")
			}
			addEv(t, w, thread, key, "run", w.runReceipt(t, 0, true))
			if runFirst {
				addEv(t, w, thread, key, "file", "out.csv")
			}
			w.v.look(w.v.agent("working", w.v.now.Unix()))
			it, _ := w.r1Page(t, key)
			kinds := map[string]bool{}
			for _, x := range it {
				kinds[x.Kind] = true
			}
			if w.step(0).Label != "check ran (receipt)" || !kinds["file"] || len(it) != 2 {
				bad = append(bad, fmt.Sprintf("run first %v: label %q, items %+v (want the receipt read and both items kept)", runFirst, w.step(0).Label, it))
			}
		}
		scenario(t, "R1-N11 run + file evidence lines: the receipt is read and both items kept, whichever comes first", &bad)
	})
}

// TestVMAgentR2NoGrandfather checks that old runs are not grandfathered. A run from before r2At (a one-time upgrade
// time kept in featsup.json) must still not meet a current, incompatible criterion: a non-run proof isn't met by a
// receipt, and a run proof's literals still apply. FIXTURE: r2At is set an hour ahead in featsup.json (as if the
// upgrade came after the run), then the panel restarts.
func TestVMAgentR2NoGrandfather(t *testing.T) {
	setR2At := func(t *testing.T, w *writerEnv) {
		w.v.e.s.sup.mu.Lock()
		f := map[string]any{}
		raw, _ := os.ReadFile(w.v.e.s.sup.file)
		json.Unmarshal(raw, &f)
		f["r2At"] = time.Now().Add(time.Hour).Unix()
		raw, _ = json.Marshal(f)
		os.WriteFile(w.v.e.s.sup.file, raw, 0o600)
		w.v.e.s.sup.mu.Unlock()
		w.restart()
	}
	for _, c := range []struct{ name, check, proof, wantHow string }{
		{"non-run proof", "printf '12 rows\\n'", "file — the CSV with 12 rows", "needs your attestation"},
		{"run proof whose literal the output lacks", "printf '3 rows\\n'", "run — exit 0 and output names `12 rows`", "mechanical check"},
	} {
		t.Run("C9 "+c.name+": a run before r2At still doesn't meet it", func(t *testing.T) {
			var bad []string
			cwd := t.TempDir()
			w := newWriterEnv(t, fmt.Sprintf("## Ship\n- [x] Export CSV @Builder\n  - covers: D1\n  - check: %s\n  - cwd: %s\n  - proof: %s\n", c.check, cwd, c.proof))
			d := w.v.e.s.featDossier(supTeam)
			writeFile(t, d.Paths["FEATURE.md"], "# Export\n\n## Desired outcomes\n- [ ] D1: CSV export works\n")
			w.v.look(w.v.agent("working", w.v.now.Unix()))
			setR2At(t, w)
			at := w.runReceipt(t, 0, true)
			w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'run', sys.argv[4])", supTeam, d.Thread, d.Milestones[0].Steps[0].Key, at)
			w.v.look(w.v.agent("working", w.v.now.Unix()))
			cr, raw := w.r2Evidence(t, d.Milestones[0].Steps[0].Key)
			if (cr.Met != nil && *cr.Met) || cr.How != c.wantHow || w.step(0).Label == "check ran (receipt)" {
				bad = append(bad, "a run from before r2At met the current criterion (a grandfather bypass): "+raw)
			}
			scenario(t, "R2-C9 "+c.name+": a run predating r2At doesn't meet the current criterion (no new grandfathering)", &bad)
		})
	}
}

// TestVMAgentR1MissingAppeared covers an attestation made over evidence that was missing, which stores a "missing"
// sentinel. When a file later appears there as a symlink to an outside or secret file, the guard must apply to the IO
// itself: the secret's bytes are never read (their sha256 appears nowhere), and a FIFO behind the link never blocks a
// look or a page read.
func TestVMAgentR1MissingAppeared(t *testing.T) {
	mk := func(t *testing.T, attest bool) (*writerEnv, string, string) {
		w := newWriterEnv(t, "## Ship\n- [x] Export CSV @Builder\n")
		w.v.e.s.cfg.pinFile = filepath.Join(w.v.e.s.cfg.home, ".config", "control-panel", "pin")
		w.v.a.mu.Lock()
		w.v.a.mu.Unlock() //nolint:staticcheck
		d := w.v.e.s.featDossier(supTeam)
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		key := d.Milestones[0].Steps[0].Key
		if out := w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'file', 'later.txt')", supTeam, d.Thread, key); !strings.HasPrefix(out, "OK") {
			t.Fatalf("add_evidence: %s", out)
		}
		if attest {
			if c := w.ownerVerify(t, 0); c != 200 {
				t.Fatalf("precondition: attest over the missing file: %d", c)
			}
		}
		return w, key, filepath.Dir(w.v.planFile())
	}
	// bounded: f must finish in 10 s; if it blocks on the FIFO, the test opens the FIFO's write end to release the
	// panel's reader (so the harness doesn't hang) and fails
	bounded := func(t *testing.T, fifo, why string, f func()) bool {
		done := make(chan struct{})
		go func() { f(); close(done) }()
		select {
		case <-done:
			return true
		case <-time.After(10 * time.Second):
			t.Errorf("SCENARIO FAILED: R1-N12 %s blocked: the panel opened what the link points to", why)
			for i := 0; i < 50; i++ {
				select {
				case <-done:
					return false
				default:
				}
				if wf, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
					wf.Close()
				}
				time.Sleep(100 * time.Millisecond)
			}
			return false
		}
	}

	t.Run("N12a appeared as a symlink to an outside secret: never read, never hashed, the attestation not kept as valid", func(t *testing.T) {
		var bad []string
		w, key, dir := mk(t, true)
		secret := filepath.Join(t.TempDir(), "id_secret")
		body := "TOPSECRET-APPEARED-" + fmt.Sprint(time.Now().UnixNano())
		writeFile(t, secret, body)
		os.Symlink(secret, filepath.Join(dir, "later.txt"))
		sum := sha256.Sum256([]byte(body))
		hexsum := hex.EncodeToString(sum[:])
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		_, raw := w.r1Page(t, key)
		if strings.Contains(raw, hexsum) || strings.Contains(raw, "TOPSECRET") || strings.Contains(w.supBytes(t), hexsum) {
			bad = append(bad, "the outside secret behind the appeared link was read and hashed (its sha256 is on the page or in featsup.json)")
		}
		if st := w.step(0); st.Label == "owner verified" && st.State == "done" {
			t.Logf("R1-N12a observation: the attestation still verifies after the link appeared (the guard refuses it as outside; whether that counts as stale is the panel's call)")
		}
		scenario(t, "R1-N12a missing->appeared as a link to an outside secret: its bytes are never read or hashed", &bad)
	})

	for _, attest := range []bool{true, false} {
		name := map[bool]string{true: "N12b attested while missing (the sentinel path)", false: "N12c control: no attestation (no sentinel)"}[attest]
		t.Run(name+": appeared as a symlink to an outside FIFO, a look and a page read don't block", func(t *testing.T) {
			w, key, dir := mk(t, attest)
			fifo := filepath.Join(t.TempDir(), "pipe")
			if err := exec.Command("mkfifo", fifo).Run(); err != nil {
				t.Skip("mkfifo unavailable")
			}
			os.Symlink(fifo, filepath.Join(dir, "later.txt"))
			ok := bounded(t, fifo, name+": a supervisor look", func() { w.v.look(w.v.agent("working", w.v.now.Unix())) })
			ok = bounded(t, fifo, name+": the owner's page read", func() { w.r1Page(t, key) }) && ok
			if ok {
				t.Log("SCENARIO ok: R1-" + name + ": nothing opened the outside FIFO")
			}
		})
	}
}
