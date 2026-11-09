package main

// Negative-contract tests for non-run evidence (files, images, logs) attached to a step, through the real routes.
// Evidence goes in with vm_mcp add_evidence (a write path, which seals the file's bytes), the agent's tool calls are
// written into its transcript the way Claude Code writes them (toolCall), and every verdict is read from the owner's
// page (page.evidence) or from vm_mcp's done. Nothing here may verify a step or meet an objective by a seal or by
// provenance alone.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// r1Item is one evidence item on the owner's page: what was cited, its seal at write time, the bytes' current state,
// and the provenance (a matching Write tool call) or mere mention found in the agent's transcript.
type r1Item struct {
	ID, Kind, Ref, Refused, Path string
	Seal                         *struct {
		Sha256, PlanRev, StepKey, SubmittedBy string
		Size, FirstSeen, CriterionAt          int64
	}
	Current *struct {
		Sha256, How, Why string
	}
	Stale      bool
	StaleWhy   string
	Provenance *struct {
		Tool, ToolUseID string
		BytesMatch      *bool
	}
	Mention        *struct{ Tool string }
	ProvenanceNone bool
}

// r1Page returns the owner's page evidence items for the step with the given key, and the raw page JSON.
func (w *writerEnv) r1Page(t *testing.T, key string) ([]r1Item, string) {
	r := w.v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", w.v.e.authed(w.v.e.login(t)))
	raw, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var f struct {
		Page struct {
			Evidence map[string]struct{ Items []r1Item }
		}
	}
	json.Unmarshal(raw, &f)
	return f.Page.Evidence[key].Items, string(raw)
}

// toolCall appends one tool_use and its tool_result to the agent's transcript, as Claude Code writes them. isErr and
// interrupted shape the result; at is the time of the result (the call itself is stamped one second earlier).
func toolCall(t *testing.T, transcript, id, name string, input map[string]any, isErr, interrupted bool, at time.Time) {
	use, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": at.Add(-time.Second).Format(time.RFC3339Nano), "sessionId": "aaaa-bbbb-1",
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}})
	res := map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"}
	if isErr {
		res["is_error"] = true
	}
	out, _ := json.Marshal(map[string]any{"type": "user", "timestamp": at.Format(time.RFC3339Nano), "sessionId": "aaaa-bbbb-1",
		"message": map[string]any{"content": []any{res}}, "toolUseResult": map[string]any{"interrupted": interrupted}})
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(append(use, '\n'), append(out, '\n')...))
	f.Close()
}

// TestVMAgentR1Evidence checks that evidence is sealed at the write and that provenance, staleness and refusals follow
// from the transcript and the bytes: forged files have no provenance, only an exact successful Write counts, changed
// bytes go stale and void an attestation, secret or outside paths are refused unread, and reads seal nothing.
func TestVMAgentR1Evidence(t *testing.T) {
	plan := "## Ship\n- [x] Export CSV @Builder\n  - covers: D1\n"
	setup := func(t *testing.T) (w *writerEnv, thread, key, dir, transcript string) {
		w = newWriterEnv(t, plan)
		// the PIN in its own folder, as on a real instance (-pin-file ~/.config/control-panel/pin): the test server keeps
		// it in its home root, which would make the whole home a secret path
		w.v.e.s.cfg.pinFile = filepath.Join(w.v.e.s.cfg.home, ".config", "control-panel", "pin")
		w.v.a.mu.Lock()
		w.v.a.mu.Unlock() //nolint:staticcheck // published through a lock the handlers take
		d := w.v.e.s.featDossier(supTeam)
		dir = filepath.Dir(w.v.planFile())
		writeFile(t, d.Paths["FEATURE.md"], "# Export\n\n## Desired outcomes\n- [ ] D1: CSV export works\n")
		w.v.look(w.v.agent("working", w.v.now.Unix())) // the Builder agent and its transcript are this feature's
		return w, d.Thread, d.Milestones[0].Steps[0].Key, dir, filepath.Join(w.v.cwd, "transcript.jsonl")
	}
	evidence := func(t *testing.T, w *writerEnv, thread, key, ref string) {
		if out := w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'file', sys.argv[4])", supTeam, thread, key, ref); !strings.HasPrefix(out, "OK") {
			t.Fatalf("add_evidence %s: %s", ref, out)
		}
	}
	notVerified := func(w *writerEnv, bad *[]string, why string) {
		if st := w.step(0); st.State == "done" || st.Label == "owner verified" || st.Label == "check ran (receipt)" {
			*bad = append(*bad, why+": the step became verified by seal/provenance: "+fmt.Sprint(st))
		}
	}

	t.Run("N1 a forged file with no tool call: sealed, provenanceNone, never verified", func(t *testing.T) {
		var bad []string
		w, thread, key, dir, _ := setup(t)
		writeFile(t, filepath.Join(dir, "export.png"), "forged")
		evidence(t, w, thread, key, "export.png")
		it, _ := w.r1Page(t, key)
		if len(it) != 1 || it[0].Seal == nil || it[0].Seal.SubmittedBy != "writer" || it[0].Seal.StepKey != key || !it[0].ProvenanceNone || it[0].Provenance != nil {
			bad = append(bad, fmt.Sprintf("forged file: %+v (want sealed by the writer for %s, provenanceNone)", it, key))
		}
		notVerified(w, &bad, "N1")
		scenario(t, "R1-N1 forged file: sealed at the write, provenanceNone, not verified", &bad)
	})

	t.Run("N2 Write after the criterion, then a hand edit: stale, and no reseal on a look or a restart", func(t *testing.T) {
		var bad []string
		w, thread, key, dir, tr := setup(t)
		p := filepath.Join(dir, "export.csv")
		writeFile(t, p, "a,b\n1,2\n")
		toolCall(t, tr, "toolu_w1", "Write", map[string]any{"file_path": p, "content": "a,b\n1,2\n"}, false, false, time.Now().Add(2*time.Second))
		evidence(t, w, thread, key, "export.csv")
		it, _ := w.r1Page(t, key)
		if len(it) != 1 || it[0].Provenance == nil || it[0].Provenance.Tool != "Write" || it[0].Provenance.BytesMatch == nil || !*it[0].Provenance.BytesMatch || it[0].Stale {
			bad = append(bad, fmt.Sprintf("after an exact Write: %+v (want Write provenance, bytesMatch, not stale)", it))
		}
		seal0 := ""
		if len(it) == 1 && it[0].Seal != nil {
			seal0 = it[0].Seal.Sha256
		}
		writeFile(t, p, "a,b\n1,999\n") // edited by hand: no tool call
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		w.restart()
		it, _ = w.r1Page(t, key)
		if len(it) != 1 || !it[0].Stale || it[0].StaleWhy != "bytes changed since sealed" || it[0].Seal == nil || it[0].Seal.Sha256 != seal0 {
			bad = append(bad, fmt.Sprintf("after a hand edit, a look and a restart: %+v (want stale, the first seal %s kept)", it, seal0))
		}
		notVerified(w, &bad, "N2")
		scenario(t, "R1-N2 exact Write -> provenance with bytesMatch; hand edit -> stale; no auto-reseal across a look and a restart", &bad)
	})

	t.Run("N3 a same-size edit with the mtime put back: never shown as current, and an attestation over it goes stale", func(t *testing.T) {
		var bad []string
		w, thread, key, dir, tr := setup(t)
		p := filepath.Join(dir, "report.txt")
		writeFile(t, p, "rows: 12\n")
		toolCall(t, tr, "toolu_w2", "Write", map[string]any{"file_path": p, "content": "rows: 12\n"}, false, false, time.Now().Add(2*time.Second))
		evidence(t, w, thread, key, "report.txt")
		if c := w.ownerVerify(t, 0); c != 200 {
			t.Fatalf("precondition: owner attest: %d", c)
		}
		w.r1Page(t, key) // a GET fills the cache with the sealed bytes
		fi, _ := os.Stat(p)
		writeFile(t, p, "rows: 99\n") // the same size
		os.Chtimes(p, fi.ModTime(), fi.ModTime())
		it, raw := w.r1Page(t, key)
		if len(it) == 1 && it[0].Current != nil && it[0].Current.How == "stat-unchanged" && !it[0].Stale {
			t.Logf("R1-N3 observation: the page shows stat-unchanged (metadata preserved) for changed bytes: %s", it[0].Current.Sha256)
		}
		if strings.Contains(raw, `"how":"hashed"`) && len(it) == 1 && !it[0].Stale {
			bad = append(bad, "the page says the changed bytes were hashed and match")
		}
		if ok, out := w.vmDone(thread); ok {
			bad = append(bad, "done accepted over an attestation whose evidence bytes changed (same size, mtime restored): "+out)
		}
		w.v.look(w.v.agent("working", w.v.now.Unix())) // a write-path look must hash, not trust stat
		if st := w.step(0); st.Label == "owner verified" && st.State == "done" {
			bad = append(bad, "after a supervisor look the attestation still verifies the step over changed bytes")
		}
		scenario(t, "R1-N3 metadata-preserving edit: never a current match on a decision path; the attestation goes stale; done refused", &bad)
	})

	t.Run("N4 a Bash run that only names the path is a mention, never provenance", func(t *testing.T) {
		var bad []string
		w, thread, key, dir, tr := setup(t)
		p := filepath.Join(dir, "shot.png")
		writeFile(t, p, "png")
		toolCall(t, tr, "toolu_b1", "Bash", map[string]any{"command": "ls -l " + p}, false, false, time.Now().Add(2*time.Second))
		evidence(t, w, thread, key, "shot.png")
		it, _ := w.r1Page(t, key)
		if len(it) != 1 || it[0].Mention == nil || it[0].Mention.Tool != "Bash" || it[0].Provenance != nil || !it[0].ProvenanceNone {
			bad = append(bad, fmt.Sprintf("a Bash mention: %+v (want mention, no provenance, provenanceNone)", it))
		}
		notVerified(w, &bad, "N4")
		scenario(t, "R1-N4 a Bash run naming the path: mention (correlation) only, provenanceNone", &bad)
	})

	t.Run("N5 a Write before the criterion, a failed Write, an interrupted Write, a Write to another path: none counts", func(t *testing.T) {
		var bad []string
		w, thread, key, dir, tr := setup(t)
		p := filepath.Join(dir, "doc.md")
		writeFile(t, p, "# doc\n")
		in := map[string]any{"file_path": p, "content": "# doc\n"}
		toolCall(t, tr, "toolu_early", "Write", in, false, false, time.Now().Add(-48*time.Hour))
		toolCall(t, tr, "toolu_err", "Write", in, true, false, time.Now().Add(2*time.Second))
		toolCall(t, tr, "toolu_int", "Write", in, false, true, time.Now().Add(3*time.Second))
		toolCall(t, tr, "toolu_other", "Write", map[string]any{"file_path": p + ".bak", "content": "# doc\n"}, false, false, time.Now().Add(4*time.Second))
		evidence(t, w, thread, key, "doc.md")
		if it, _ := w.r1Page(t, key); len(it) != 1 || it[0].Provenance != nil || !it[0].ProvenanceNone {
			got := ""
			if len(it) == 1 && it[0].Provenance != nil {
				got = it[0].Provenance.ToolUseID
				if it[0].Seal != nil {
					got += fmt.Sprintf(", seal criterionAt %d (the early Write was 48 h ago)", it[0].Seal.CriterionAt)
				}
			}
			bad = append(bad, fmt.Sprintf("only early/failed/interrupted/other-path Writes, but provenance = %q (want provenanceNone)", got))
		}
		scenario(t, "R1-N5 early, failed, interrupted or other-path Writes don't count as provenance", &bad)
	})

	t.Run("N6 outside, symlink out, .env, .git: refused like outside, never named, never hashed; PLAN.md unchanged on refusal", func(t *testing.T) {
		var bad []string
		// a fresh feature per ref, so each ref's evidence item is read on its own
		for _, ref := range []string{"OUTSIDE", "link-out.txt", ".env", ".git/config", "../../etc/hostname"} {
			w, thread, key, dir, _ := setup(t)
			outside := filepath.Join(t.TempDir(), "secret-outside.txt")
			writeFile(t, outside, "TOPSECRET-OUT")
			os.Symlink(outside, filepath.Join(dir, "link-out.txt"))
			writeFile(t, filepath.Join(dir, ".env"), "API_KEY=TOPSECRET-ENV")
			writeFile(t, filepath.Join(dir, ".git", "config"), "[core]\n")
			if ref == "OUTSIDE" {
				ref = outside
			}
			evidence(t, w, thread, key, ref)
			it, raw := w.r1Page(t, key)
			if len(it) != 1 || it[0].Refused != "outside the feature's folders" || it[0].Path != "" || it[0].Seal != nil || it[0].Current != nil {
				bad = append(bad, fmt.Sprintf("%q: %+v (want refused as outside, no path, no seal, no hash)", ref, it))
			}
			if strings.Contains(raw, "TOPSECRET") || strings.Contains(strings.ReplaceAll(raw, `\/`, "/"), filepath.Join(dir, ".env")) {
				bad = append(bad, fmt.Sprintf("%q: the page leaks a secret's path or bytes", ref))
			}
			if strings.Contains(w.supBytes(t), "TOPSECRET") {
				bad = append(bad, fmt.Sprintf("%q: a secret's bytes reached featsup.json", ref))
			}
		}
		scenario(t, "R1-N6 outside/symlink-out/.env/.git refused as outside: no path, no seal, no hash, nothing leaked", &bad)
	})

	t.Run("N7 GETs seal nothing", func(t *testing.T) {
		var bad []string
		w, _, key, dir, _ := setup(t)
		cur, _ := os.ReadFile(w.v.planFile()) // the evidence line written by hand: no writer call, nothing sealed yet
		writeFile(t, filepath.Join(dir, "x.png"), "x")
		writeFile(t, w.v.planFile(), strings.Replace(string(cur), "  - covers: D1\n", "  - covers: D1\n  - evidence: file x.png\n", 1))
		before := w.supBytes(t)
		w.r1Page(t, key)
		w.get(t, "/agent/feature?session="+supTeam)
		w.py("m.get_feature_plan(sys.argv[1])", supTeam)
		w.py("m.feature_record(sys.argv[1])", supTeam)
		if w.supBytes(t) != before {
			bad = append(bad, "a GET wrote featsup.json (a first-sight seal on read)")
		}
		if it, _ := w.r1Page(t, key); len(it) == 1 && it[0].Seal != nil && it[0].Seal.SubmittedBy != "look" {
			bad = append(bad, fmt.Sprintf("a seal appeared without a write path: %+v", it[0].Seal))
		}
		scenario(t, "R1-N7 page, agent record and vm_mcp reads seal nothing", &bad)
	})

	t.Run("N8 an owner attestation binds its evidence seals: a change makes it stale, the objective unmet, done refused", func(t *testing.T) {
		var bad []string
		w, thread, key, dir, tr := setup(t)
		p := filepath.Join(dir, "proof.png")
		writeFile(t, p, "png-v1")
		toolCall(t, tr, "toolu_w8", "Write", map[string]any{"file_path": p, "content": "png-v1"}, false, false, time.Now().Add(2*time.Second))
		evidence(t, w, thread, key, "proof.png")
		if c := w.ownerVerify(t, 0); c != 200 {
			t.Fatalf("precondition: attest: %d", c)
		}
		writeFile(t, p, "png-v2-longer")
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		if st := w.step(0); st.State == "done" || st.Label == "owner verified" {
			bad = append(bad, fmt.Sprintf("after the evidence changed the attestation still verifies: %+v", st))
		}
		if p := w.vmObjPage(t); len(p.Objectives.Items) == 1 && p.Objectives.Items[0].Met {
			bad = append(bad, "D1 still met over changed evidence")
		}
		if ok, out := w.vmDone(thread); ok {
			bad = append(bad, "done accepted: "+out)
		}
		scenario(t, "R1-N8 attestation binds seals: changed bytes -> stale attestation, step claimed, D1 unmet, done refused", &bad)
	})

	t.Run("N9 several evidence lines on one step: each its own item, id and seal; a change marks only that one stale", func(t *testing.T) {
		var bad []string
		w, thread, key, dir, tr := setup(t)
		img, lg := filepath.Join(dir, "shot.png"), filepath.Join(dir, "run.log")
		writeFile(t, img, "png-bytes")
		writeFile(t, lg, "12 rows exported\n")
		toolCall(t, tr, "toolu_i", "Write", map[string]any{"file_path": img, "content": "png-bytes"}, false, false, time.Now().Add(2*time.Second))
		toolCall(t, tr, "toolu_l", "Write", map[string]any{"file_path": lg, "content": "12 rows exported\n"}, false, false, time.Now().Add(3*time.Second))
		w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'image', 'shot.png')", supTeam, thread, key)
		w.py("m.add_evidence(sys.argv[1], sys.argv[2], sys.argv[3], 'file', 'run.log')", supTeam, thread, key)
		it, _ := w.r1Page(t, key)
		ids := map[string]bool{}
		for _, x := range it {
			ids[x.ID] = true
		}
		if len(it) != 2 || len(ids) != 2 || it[0].Seal == nil || it[1].Seal == nil || it[0].Seal.Sha256 == it[1].Seal.Sha256 {
			t.Fatalf("two evidence lines: %+v (want two items, distinct ids, each sealed)", it)
		}
		writeFile(t, lg, "0 rows exported\n")
		it, _ = w.r1Page(t, key)
		stale := map[string]bool{}
		for _, x := range it {
			stale[x.Ref] = x.Stale
		}
		if len(it) != 2 || !stale["run.log"] || stale["shot.png"] {
			bad = append(bad, fmt.Sprintf("after the log changed: %+v (want only run.log stale)", it))
		}
		notVerified(w, &bad, "N9")
		scenario(t, "R1-N9 two evidence lines: two items with their own ids and seals; only the changed one is stale", &bad)
	})

	t.Run("N10 a legacy 'evidence: log <ref>' line (written before the client used file) stays an evidence item, not lost", func(t *testing.T) {
		var bad []string
		w, _, key, dir, tr := setup(t)
		p := filepath.Join(dir, "run.log")
		writeFile(t, p, "12 rows exported\n")
		toolCall(t, tr, "toolu_log", "Write", map[string]any{"file_path": p, "content": "12 rows exported\n"}, false, false, time.Now().Add(2*time.Second))
		cur, _ := os.ReadFile(w.v.planFile())
		if c, out := w.fdoc(strings.Replace(string(cur), "  - covers: D1\n", "  - covers: D1\n  - evidence: log run.log\n", 1), "put", "plan", "-", "--rev", w.rev(t, "plan")); c != 0 {
			t.Fatalf("legacy line: %s", out)
		}
		it, _ := w.r1Page(t, key)
		if len(it) != 1 || it[0].Ref != "run.log" || it[0].Seal == nil || it[0].Path == "" || (it[0].Kind != "file" && it[0].Kind != "log") {
			bad = append(bad, fmt.Sprintf("the legacy log line on the page: %+v (want an evidence item for run.log, kind file or log, sealed with its path)", it))
		}
		notVerified(w, &bad, "N10")
		scenario(t, "R1-N10 a legacy 'evidence: log' line stays a sealed evidence item (parser compatibility), never an unparsed or dropped line", &bad)
	})
}
