package main

// Tests for the structured log metadata and the explicit artifacts on the feature page. The client side (vm_mcp
// report_feature_progress / link_artifact and bin/feature-doc log / artifact) runs against the panel's real handlers.
// TestVMAgentLogMetadata checks that authored fields (ran, next, objectives, pr, proof step, session) land on page.log
// exactly as written. TestVMAgentArtifacts checks that linked artifacts appear on page.arts and that unsafe paths are
// refused. Claims stay claims: nothing here verifies a step, meets an objective or changes the plan.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// laRan is one "ran" item of a log entry: a command, its result, and whether it succeeded (nil when not stated).
type laRan struct {
	Cmd    string `json:"cmd"`
	Result string `json:"result"`
	Ok     *bool  `json:"ok"`
}

// laLog is the part of a page.log entry these tests read. Fields ending in Claimed hold values the panel could not
// verify; raw keeps the entry's JSON for failure messages.
type laLog struct {
	Kind, Title, Step, Next, ProofStep, ProofStepClaimed, Session string
	Ran                                                           []laRan
	RanUnparsed                                                   []string
	Objectives, ObjectivesClaimed                                 []string
	Pr                                                            int
	PrKnown                                                       *bool
	SessionClaimed                                                bool
	Considered                                                    []map[string]any
	Chose                                                         map[string]any
	raw                                                           string
}

// laPage loads the owner's feature page and returns its parsed log entries, its artifact rows and the raw response.
func (w *writerEnv) laPage(t *testing.T) (logs []laLog, arts []map[string]any, raw string) {
	t.Helper()
	w.v.look(w.v.agent("working", w.v.now.Unix()))
	r := w.v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", w.v.e.authed(w.v.e.login(t)))
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var f struct {
		Page struct {
			Log  []json.RawMessage `json:"log"`
			Arts []map[string]any  `json:"arts"`
		} `json:"page"`
	}
	json.Unmarshal(b, &f)
	for _, l := range f.Page.Log {
		var e laLog
		json.Unmarshal(l, &e)
		e.raw = string(l)
		logs = append(logs, e)
	}
	return logs, f.Page.Arts, string(b)
}

// laFind returns the log entry with the given title, or nil if there is none.
func laFind(logs []laLog, title string) *laLog {
	for i := range logs {
		if logs[i].Title == title {
			return &logs[i]
		}
	}
	return nil
}

// laLogPost posts a raw line to the agent log route with the agent token, as the client would, and returns the HTTP
// status. It is used for lines the client itself would not write.
func (w *writerEnv) laLogPost(t *testing.T, line string) int {
	b, _ := json.Marshal(map[string]string{"session": supTeam, "line": line})
	req, _ := http.NewRequest("POST", w.url+"/agent/feature/log", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer contract-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// featureDoc runs bin/feature-doc for the test feature against the test panel, as the agent session sid, and returns
// its combined output.
func (w *writerEnv) featureDoc(t *testing.T, sid string, args ...string) (string, error) {
	c := exec.Command("python3", append([]string{filepath.Join(w.vm, "bin", "feature-doc"), "--session", supTeam}, args...)...)
	c.Env = append(os.Environ(), "PANEL_AGENT_URL="+w.url, "PANEL_AGENT_TOKEN_FILE="+w.tok, "CLAUDE_CODE_SESSION_ID="+sid)
	out, err := c.CombinedOutput()
	return string(out), err
}

// TestVMAgentLogMetadata checks that log lines written through report_feature_progress and feature-doc log land on
// page.log with every authored field intact, that unknown references are labelled rather than dropped, that delimiters
// in a command cannot forge fields, and that over-long lines are refused rather than cut.
func TestVMAgentLogMetadata(t *testing.T) {
	plan := "## Ship\n- [ ] Export CSV @Builder\n- [ ] Write the README @Docs\n"
	setup := func(t *testing.T) (*writerEnv, featDossier) {
		w := newWriterEnv(t, plan)
		writeFile(t, filepath.Join(filepath.Dir(w.v.planFile()), "FEATURE.md"), "# Export\n\n## Desired outcomes\n- [ ] D1: CSV export works — done when: a CSV opens\n")
		return w, w.v.e.s.featDossier(supTeam)
	}
	planBytes := func(w *writerEnv) string { b, _ := os.ReadFile(w.v.planFile()); return string(b) }

	t.Run("L1 every authored field lands exactly on page.log; L5 nothing verified, plan unchanged", func(t *testing.T) {
		var bad []string
		w, d := setup(t)
		key, before := d.Milestones[0].Steps[0].Key, planBytes(w)
		out := w.py("m.report_feature_progress(sys.argv[1], 'action', 'exported the CSV', step=sys.argv[2], thread=sys.argv[3], "+
			"ran=[{'cmd': 'go test ./...', 'result': 'ok 12 rows', 'ok': True}, {'cmd': 'curl -s x', 'result': '', 'ok': False}, {'cmd': 'ls'}], "+
			"next='wire the page', objectives=['D1'], pr=418, proof_step=sys.argv[2])", supTeam, key, d.Thread)
		if !strings.HasPrefix(out, "OK logged") {
			t.Fatalf("report: %s", out)
		}
		logs, _, raw := w.laPage(t)
		l := laFind(logs, "exported the CSV")
		if l == nil {
			t.Fatalf("no page.log entry: %s", raw)
		}
		tr, fa := true, false
		want := []laRan{{"go test ./...", "ok 12 rows", &tr}, {"curl -s x", "", &fa}, {"ls", "", nil}}
		if len(l.Ran) != 3 {
			bad = append(bad, "ran: "+l.raw)
		} else {
			for i, r := range want {
				g := l.Ran[i]
				if g.Cmd != r.Cmd || g.Result != r.Result || (g.Ok == nil) != (r.Ok == nil) || (g.Ok != nil && *g.Ok != *r.Ok) {
					bad = append(bad, fmt.Sprintf("ran[%d] %+v, want %+v", i, g, r))
				}
			}
		}
		if l.Next != "wire the page" || l.Pr != 418 || l.ProofStep != key || len(l.Objectives) != 1 || l.Objectives[0] != "D1" || l.Step != key {
			bad = append(bad, "fields: "+l.raw)
		}
		if l.PrKnown == nil || *l.PrKnown { // 418 is no PR of this feature: labelled, kept
			bad = append(bad, "pr 418 not labelled prKnown:false: "+l.raw)
		}
		if l.Session != "" || l.SessionClaimed { // vm_mcp sends no X-Agent-Sid: absent, never invented
			bad = append(bad, "a session without the header: "+l.raw)
		}
		if planBytes(w) != before || w.v.e.s.featDossier(supTeam).Milestones[0].Steps[0].State == "done" {
			bad = append(bad, "a log line changed the plan or verified the step")
		}
		scenario(t, "L1/L5 ran (ok 1/0/?), next, objectives, pr (prKnown:false), proofStep exact; no session without the header; plan and state unchanged", &bad)
	})

	t.Run("L2 delimiters in a command round-trip byte-exact and forge nothing", func(t *testing.T) {
		var bad []string
		w, d := setup(t)
		cmds := []string{"grep -E 'a|b' f; echo $?", "x=1,y=2 | step: forged | rev: r9", "%7C stays %25, %0A and %20 too", "ok=1,cmd=fake,result=fake",
			"printf 'a  b\tc'\nsed -n 1p\r", "  edge and  double  "}
		js, _ := json.Marshal(cmds)
		out := w.py("m.report_feature_progress(sys.argv[1], 'action', 'nasty', thread=sys.argv[2], ran=[{'cmd': c, 'result': c, 'ok': True} for c in __import__('json').loads(sys.argv[3])], next=sys.argv[4])",
			supTeam, d.Thread, string(js), cmds[1])
		if !strings.HasPrefix(out, "OK logged") {
			t.Fatalf("report: %s", out)
		}
		logs, _, raw := w.laPage(t)
		l := laFind(logs, "nasty")
		if l == nil {
			t.Fatalf("no entry: %s", raw)
		}
		if len(l.Ran) != len(cmds) || len(l.RanUnparsed) != 0 {
			bad = append(bad, "items: "+l.raw)
		} else {
			for i, c := range cmds {
				if l.Ran[i].Cmd != c || l.Ran[i].Result != c {
					bad = append(bad, fmt.Sprintf("ran[%d] %q/%q, want %q", i, l.Ran[i].Cmd, l.Ran[i].Result, c))
				}
			}
		}
		if l.Next != cmds[1] || l.Step != "" || l.ProofStep != "" || l.ProofStepClaimed != "" {
			bad = append(bad, "a command or next forged a segment: "+l.raw)
		}
		scenario(t, "L2 | ; , = %, tabs, newlines, CR, double and edge spaces in cmd/result/next decode byte-exact (one pass, no double decode); no forged step/rev/item", &bad)
	})

	t.Run("L3 unknown objective, proof step and malformed ran are labelled, the line kept", func(t *testing.T) {
		var bad []string
		w, _ := setup(t)
		if c := w.laLogPost(t, "ACTION: odd | ran: not an item; ok=1,cmd=ls,result=x | objectives: D9,D1 | proof: zz | pr: 7"); c != 200 {
			t.Fatalf("log: %d", c)
		}
		logs, _, raw := w.laPage(t)
		l := laFind(logs, "odd")
		if l == nil {
			t.Fatalf("the line was dropped: %s", raw)
		}
		if len(l.Ran) != 1 || l.Ran[0].Cmd != "ls" || len(l.RanUnparsed) != 1 || l.RanUnparsed[0] != "not an item" {
			bad = append(bad, "ran/ranUnparsed: "+l.raw)
		}
		if strings.Join(l.Objectives, ",") != "D1" || strings.Join(l.ObjectivesClaimed, ",") != "D9" {
			bad = append(bad, "objectives: "+l.raw)
		}
		if l.ProofStep != "" || l.ProofStepClaimed != "zz" || l.Pr != 7 || l.PrKnown == nil || *l.PrKnown {
			bad = append(bad, "proof/pr labels: "+l.raw)
		}
		scenario(t, "L3 objectivesClaimed, proofStepClaimed, prKnown:false, ranUnparsed raw; never dropped", &bad)
	})

	t.Run("L4 session is the X-Agent-Sid, labelled claimed; a forged by: doesn't change it", func(t *testing.T) {
		var bad []string
		w, _ := setup(t)
		if out, err := w.featureDoc(t, "sid-w1", "log", "ACTION: by a worker | by: @Owner", "--next", "the README"); err != nil {
			t.Fatalf("feature-doc log: %v %s", err, out)
		}
		logs, _, raw := w.laPage(t)
		l := laFind(logs, "by a worker")
		if l == nil {
			t.Fatalf("no entry: %s", raw)
		}
		if l.Session != "sid-w1" || !l.SessionClaimed || l.Next != "the README" {
			bad = append(bad, "session/claimed: "+l.raw)
		}
		scenario(t, "L4 session = X-Agent-Sid with sessionClaimed:true (attribution, not authority)", &bad)
	})

	t.Run("L6 an old-grammar line reads as before; L7 over 2000 characters is refused, not cut", func(t *testing.T) {
		var bad []string
		w, d := setup(t)
		key := d.Milestones[0].Steps[1].Key
		if out, err := w.featureDoc(t, "", "log", "DECISION: docs first | considered: docs; region | chose: docs | step: "+key); err != nil {
			t.Fatalf("feature-doc log: %v %s", err, out)
		}
		logs, _, raw := w.laPage(t)
		l := laFind(logs, "docs first")
		if l == nil || len(l.Considered) != 2 || l.Chose["option"] != "docs" || l.Step != key || l.Ran != nil || l.Next != "" || l.Objectives != nil || l.Pr != 0 {
			bad = append(bad, "the old line changed: "+raw)
		}
		lb := filepath.Join(filepath.Dir(w.v.planFile()), "LOG.md")
		before, _ := os.ReadFile(lb)
		c := w.laLogPost(t, "ACTION: "+strings.Repeat("y", 2000))
		after, _ := os.ReadFile(lb)
		if c != 400 || string(after) != string(before) {
			bad = append(bad, fmt.Sprintf("a 2008-character line: %d, LOG.md changed %v (must be refused, never cut)", c, string(after) != string(before)))
		}
		if out, err := w.featureDoc(t, "", "log", "ACTION: "+strings.Repeat("y", 2001)); err == nil {
			bad = append(bad, "feature-doc wrote an over-long line: "+out)
		}
		scenario(t, "L6 the 755529f grammar parses unchanged; L7 >2000 refused (400) with LOG.md unchanged", &bad)
	})
}

// laArtsEnv returns a writer environment whose PIN file sits in its own folder, as on a real instance (-pin-file
// ~/.config/control-panel/pin). The test server otherwise keeps it in the home root, which would make the whole home a
// secret path.
func laArtsEnv(t *testing.T, plan string) *writerEnv {
	w := newWriterEnv(t, plan)
	w.v.e.s.cfg.pinFile = filepath.Join(w.v.e.s.cfg.home, ".config", "control-panel", "pin")
	w.v.a.mu.Lock()
	w.v.a.mu.Unlock() //nolint:staticcheck // published through a lock the handlers take
	return w
}

// TestVMAgentArtifacts checks that link_artifact rows appear on page.arts under their step and never count as
// evidence, that outside, secret and symlinked-out paths are listed as refused (never hashed), that over-long titles
// are refused, and that a link for another thread is rejected.
func TestVMAgentArtifacts(t *testing.T) {
	plan := "## Ship\n- [ ] Export CSV @Builder\n- [ ] Write the README @Docs\n"

	t.Run("A1 link_artifact: one row per line, distinct ids, under its step; A3 never evidence, nothing verified", func(t *testing.T) {
		var bad []string
		w := laArtsEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		key, dir := d.Milestones[0].Steps[0].Key, filepath.Dir(w.v.planFile())
		if _, arts, _ := w.laPage(t); len(arts) != 0 {
			bad = append(bad, fmt.Sprintf("arts without any artifact line: %v", arts))
		}
		writeFile(t, filepath.Join(dir, "ui", "dialog.png"), "png-bytes")
		writeFile(t, filepath.Join(dir, "ui", "bug.png"), "png-bug")
		for _, a := range [][]string{{"ui/dialog.png", "ui", "Export dialog"}, {"ui/bug.png", "issue", "Broken header"}} {
			if out := w.py("m.link_artifact(sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5], sys.argv[6])", append([]string{supTeam, d.Thread, key}, a...)...); !strings.HasPrefix(out, "OK artifact linked") {
				t.Fatalf("link_artifact: %s", out)
			}
		}
		_, arts, raw := w.laPage(t)
		if len(arts) != 2 || arts[0]["id"] == arts[1]["id"] || arts[0]["id"] == nil {
			t.Fatalf("arts: %s", raw)
		}
		for i, want := range [][]string{{"ui/dialog.png", "ui", "Export dialog"}, {"ui/bug.png", "issue", "Broken header"}} {
			a := arts[i]
			if a["step"] != key || a["purpose"] != want[1] || a["title"] != want[2] || a["state"] != "ok" || !strings.HasSuffix(fmt.Sprint(a["path"]), want[0]) {
				bad = append(bad, fmt.Sprintf("arts[%d] %v", i, a))
			}
		}
		st := w.v.e.s.featDossier(supTeam).Milestones[0].Steps[0]
		if st.State == "done" || st.Label == "owner verified" || st.Label == "check ran (receipt)" {
			bad = append(bad, "an artifact verified the step: "+st.Label)
		}
		if strings.Contains(raw, `"evidence":{"`+key) {
			bad = append(bad, "an artifact appeared as evidence")
		}
		scenario(t, "A1/A3 two artifact lines give two page.arts rows (step, purpose, title, path, distinct ids); absent with none; never evidence", &bad)
	})

	t.Run("A2 outside, secret and symlinked-out refs are refused with a reason, never hashed", func(t *testing.T) {
		var bad []string
		w := laArtsEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		key, dir := d.Milestones[0].Steps[0].Key, filepath.Dir(w.v.planFile())
		secret := "TOP-SECRET-artifact-bytes"
		out := filepath.Join(t.TempDir(), "outside.png")
		writeFile(t, out, secret)
		writeFile(t, filepath.Join(dir, ".env"), secret)
		os.Symlink(out, filepath.Join(dir, "link.png"))
		for _, p := range []string{out, ".env", "link.png"} {
			if o := w.py("m.link_artifact(sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], 'issue', 'x')", supTeam, d.Thread, key, p); !strings.HasPrefix(o, "OK") {
				bad = append(bad, "the client refused what the panel must judge: "+o)
			}
		}
		sum := sha256.Sum256([]byte(secret))
		_, arts, raw := w.laPage(t)
		if strings.Contains(raw, hex.EncodeToString(sum[:])) || strings.Contains(raw, secret) {
			bad = append(bad, "an outside/secret artifact's bytes reached the page")
		}
		for _, a := range arts {
			if a["state"] != "refused" || a["why"] == nil || a["why"] == "" || a["path"] != nil || a["size"] != float64(0) {
				bad = append(bad, fmt.Sprintf("not refused with a reason and no path: %v", a))
			}
		}
		if len(arts) != 3 {
			bad = append(bad, "refused artifacts must still be listed (with a reason): "+raw)
		}
		scenario(t, "A2 outside root, .env and symlink-out: listed refused with a reason; their bytes and hash never on the page", &bad)
	})

	t.Run("A5 a title over 200: the writer refuses it atomically (400); one already on disk shows refused, never shortened", func(t *testing.T) {
		var bad []string
		w := laArtsEnv(t, plan)
		d := w.v.e.s.featDossier(supTeam)
		dir := filepath.Dir(w.v.planFile())
		writeFile(t, filepath.Join(dir, "ui", "a.png"), "png")
		long := strings.Repeat("t", 250) // within the writer's 500-character line, over the 200-character title
		text := strings.Replace(plan, "- [ ] Export CSV @Builder\n", "- [ ] Export CSV @Builder\n  - artifact: ui/a.png | ui | "+long+"\n", 1)
		before, _ := os.ReadFile(w.v.planFile())
		b, _ := json.Marshal(map[string]string{"session": supTeam, "doc": "plan", "text": text, "rev": d.Revs["plan"], "thread": d.Thread})
		req, _ := http.NewRequest("POST", w.url+"/agent/feature/doc", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer contract-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if after, _ := os.ReadFile(w.v.planFile()); resp.StatusCode != 400 || string(after) != string(before) {
			bad = append(bad, fmt.Sprintf("the writer took a 250-character title: %d, PLAN.md changed %v (must be 400, atomic)", resp.StatusCode, string(after) != string(before)))
		}
		if o := w.py("m.link_artifact(sys.argv[1], sys.argv[2], sys.argv[3], 'ui/a.png', 'ui', sys.argv[4])", supTeam, d.Thread, d.Milestones[0].Steps[0].Key, long); !strings.HasPrefix(o, "REFUSED") {
			bad = append(bad, "the client wrote a 250-character title: "+o)
		}
		// a legacy line already on disk (written before the rule, or by hand): shown refused with a why, never cut
		writeFile(t, w.v.planFile(), text)
		_, arts, raw := w.laPage(t)
		if len(arts) != 1 {
			t.Fatalf("arts: %s", raw)
		}
		if arts[0]["state"] != "refused" || arts[0]["why"] == nil || arts[0]["why"] == "" || arts[0]["path"] != nil {
			bad = append(bad, fmt.Sprintf("a legacy 250-character title isn't refused with a why: %v", arts[0]))
		}
		if title := fmt.Sprint(arts[0]["title"]); title != "" && title != "<nil>" && title != long {
			bad = append(bad, fmt.Sprintf("the title shown shortened to %d characters", len(title)))
		}
		scenario(t, "A5 title > 200: writer 400 atomic, client refuses, a legacy line shows refused with a why, never a shortened title", &bad)
	})

	t.Run("A4 another thread's link is refused; PLAN.md unchanged", func(t *testing.T) {
		var bad []string
		w := laArtsEnv(t, plan)
		key := w.v.e.s.featDossier(supTeam).Milestones[0].Steps[0].Key
		before, _ := os.ReadFile(w.v.planFile())
		if out := w.py("m.link_artifact(sys.argv[1], 'someone-else', sys.argv[2], 'ui/a.png', 'ui', 'x')", supTeam, key); !strings.HasPrefix(out, "REFUSED") {
			bad = append(bad, "another thread's link: "+out)
		}
		if after, _ := os.ReadFile(w.v.planFile()); string(after) != string(before) {
			bad = append(bad, "a refused link wrote PLAN.md")
		}
		scenario(t, "A4 the thread guard refuses another thread's artifact link; nothing written", &bad)
	})
}
