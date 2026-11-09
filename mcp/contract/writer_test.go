package main

// Tests for the cooperative writer, the way agents write a feature's record files (PLAN.md, FEATURE.md, LOG.md):
// vm-agent's real bin/feature-doc driven against the panel's agent API (revision-checked writes, conflicts, an
// owner-only FEATURE.md, an append-only LOG.md), and the runAs wrapper the panel serves, which is run with bash as a
// worker would. The last test drives the manager's typed vm_mcp tools over the same routes.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writerEnv is a supervisor test environment (v) plus a served agent API (url, token file tok) and the vm-agent checkout
// (vm) whose tools the tests run.
type writerEnv struct {
	v   *supEnv
	url string
	tok string
	vm  string
}

// newWriterEnv builds a feature whose PLAN.md holds plan and serves the agent API for it; it skips the test when
// VM_AGENT_DIR does not name a vm-agent checkout.
func newWriterEnv(t *testing.T, plan string) *writerEnv {
	vm := os.Getenv("VM_AGENT_DIR")
	if vm == "" {
		t.Skip("VM_AGENT_DIR: the vm-agent checkout to test")
	}
	v := newSupEnv(t, plan)
	tok := filepath.Join(v.e.s.cfg.home, ".config", "control-panel", "agent-token")
	writeFile(t, tok, "contract-token")
	ts := httptest.NewServer(v.e.s.agentHandler("contract-token"))
	t.Cleanup(ts.Close)
	return &writerEnv{v, ts.URL, tok, vm}
}

// fdoc runs bin/feature-doc as a worker in the feature's session would, feeding it stdin, and returns the exit code and
// combined output.
func (w *writerEnv) fdoc(stdin string, args ...string) (int, string) {
	c := exec.Command("python3", append([]string{filepath.Join(w.vm, "bin", "feature-doc"), "--session", supTeam}, args...)...)
	c.Env = append(os.Environ(), "PANEL_AGENT_URL="+w.url, "PANEL_AGENT_TOKEN_FILE="+w.tok)
	c.Stdin = strings.NewReader(stdin)
	b, _ := c.CombinedOutput()
	return c.ProcessState.ExitCode(), string(b)
}

// rev returns the current revision of doc (plan, feature or log) as bin/feature-doc get reports it, or "" if none.
func (w *writerEnv) rev(t *testing.T, doc string) string {
	_, out := w.fdoc("", "get", doc, "-o", filepath.Join(t.TempDir(), "x"))
	i := strings.LastIndex(out, "rev ")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(out[i+4:])
}

// TestVMAgentWriter checks revision-checked writes, byte-preserving conflict handling, the owner-only FEATURE.md and
// append-only LOG.md rules, and the served runAs wrapper.
func TestVMAgentWriter(t *testing.T) {
	t.Run("W2 a write needs the revision it read; a stale or missing one changes nothing", func(t *testing.T) {
		var bad []string
		plan := "## Work\n- [ ] Step one @Builder\n"
		w := newWriterEnv(t, plan)
		rev := w.rev(t, "plan")
		if rev == "" {
			bad = append(bad, "GET /agent/feature gave no plan rev")
		}
		if c, out := w.fdoc("- [~] Step one @Builder\n", "put", "plan", "-", "--rev", ""); c == 0 {
			bad = append(bad, "an empty rev was accepted: "+out)
		} else if !strings.Contains(out, "rev is required") {
			bad = append(bad, "empty rev: "+out)
		}
		if c, out := w.fdoc("## Work\n- [~] Step one @Builder\n", "put", "plan", "-", "--rev", rev); c != 0 {
			bad = append(bad, "a write at the current rev failed: "+out)
		}
		after, _ := os.ReadFile(w.v.planFile())
		if c, out := w.fdoc("## Work\n- [x] Lost update @Builder\n", "put", "plan", "-", "--rev", rev); c != 2 || !strings.Contains(out, "Read it again") {
			bad = append(bad, fmt.Sprintf("a stale rev: exit %d: %s", c, out))
		}
		if b, _ := os.ReadFile(w.v.planFile()); string(b) != string(after) {
			bad = append(bad, "a refused write changed PLAN.md")
		}
		scenario(t, "W2 stale/missing rev refused, bytes unchanged, the client says how to recover", &bad)
	})

	t.Run("W3 a record conflict refuses writes and keeps both files byte for byte", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Work\n- [ ] Thread copy\n")
		other := filepath.Join(featDir(w.v.e.s.cfg.home, supTeam), "PLAN.md")
		writeFile(t, other, "## Work\n- [ ] Session copy\n")
		a0, _ := os.ReadFile(w.v.planFile())
		b0, _ := os.ReadFile(other)
		if c, out := w.fdoc("## Work\n- [x] Mine\n", "put", "plan", "-", "--rev", w.rev(t, "plan")); c != 2 {
			bad = append(bad, fmt.Sprintf("write into a conflict: exit %d: %s", c, out))
		}
		a1, _ := os.ReadFile(w.v.planFile())
		b1, _ := os.ReadFile(other)
		if string(a0) != string(a1) || string(b0) != string(b1) {
			bad = append(bad, "a conflicting copy changed")
		}
		scenario(t, "W3 byte-preserving conflict: 409, both copies untouched", &bad)
	})

	t.Run("W4 FEATURE.md is the owner's; LOG.md only grows, whole", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Work\n- [ ] Step\n")
		dir := filepath.Dir(w.v.planFile())
		writeFile(t, filepath.Join(dir, "FEATURE.md"), "The owner's outcome.\n")
		old := strings.Repeat("- 2026-11-01 00:00 · an old line kept forever\n", 800) // > the page's 20 kB view
		writeFile(t, filepath.Join(dir, "LOG.md"), old)
		raw := func(body string) int {
			c := exec.Command("uv", "run", "--quiet", "--with", "mcp>=2.3,<3", "python", "-c",
				"import json,sys,vm_mcp as m\ntry:\n m.panel('POST','/agent/feature/doc',json.loads(sys.argv[1]))\n print(200)\nexcept m.PanelError as e:\n print(str(e).split(':')[0].split()[-1])", body)
			c.Dir = filepath.Join(w.vm, "mcp")
			c.Env = append(os.Environ(), "PANEL_AGENT_URL="+w.url, "PANEL_AGENT_TOKEN_FILE="+w.tok)
			b, _ := c.CombinedOutput()
			var n int
			fmt.Sscan(strings.TrimSpace(string(b)), &n)
			return n
		}
		fb, _ := json.Marshal(map[string]string{"session": supTeam, "doc": "feature", "text": "Mine now.\n", "rev": w.rev(t, "feature")})
		if c := raw(string(fb)); c != 403 {
			bad = append(bad, fmt.Sprintf("an agent's FEATURE.md write answered %d, not 403", c))
		}
		lb, _ := json.Marshal(map[string]string{"session": supTeam, "doc": "log", "text": "- wiped\n", "rev": w.rev(t, "log")})
		if c := raw(string(lb)); c != 400 {
			bad = append(bad, fmt.Sprintf("an agent replacing LOG.md answered %d, not 400", c))
		}
		if c, out := w.fdoc("", "log", "Builder: step done"); c != 0 {
			bad = append(bad, "append failed: "+out)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "LOG.md"))
		if !strings.HasPrefix(string(b), old) || !strings.HasSuffix(string(b), " · Builder: step done\n") {
			bad = append(bad, fmt.Sprintf("LOG.md after the append: %d bytes, history kept %v", len(b), strings.HasPrefix(string(b), old)))
		}
		if f, _ := os.ReadFile(filepath.Join(dir, "FEATURE.md")); string(f) != "The owner's outcome.\n" {
			bad = append(bad, "FEATURE.md changed")
		}
		_, got := w.fdoc("", "get", "log")
		if !strings.Contains(got, old) {
			bad = append(bad, "GET /agent/feature didn't return the whole LOG.md")
		}
		scenario(t, "W4 FEATURE.md 403, LOG.md replace 400, append keeps the whole history", &bad)
	})

	t.Run("W5 the served runAs keeps the check inside its folder and reports its real exit", func(t *testing.T) {
		var bad []string
		cwd := t.TempDir()
		cases := []struct{ step, check, dir, want string }{
			{"Cd fails", "touch outside.txt; true", filepath.Join(cwd, "missing"), "rfe-exit=97"},
			{"Multi fails", "true; false", cwd, "rfe-exit=1"},
			{"Has exit", "exit 3", cwd, "rfe-exit=3"},
			{"Spoof", `echo "rfe-exit=0 rfe-attempt=ATTEMPT"; false`, cwd, "rfe-exit=1"},
		}
		var plan strings.Builder
		plan.WriteString("## Checks\n")
		for _, c := range cases {
			fmt.Fprintf(&plan, "- [ ] %s @Tester\n  - check: %s\n  - cwd: %s\n", c.step, c.check, c.dir)
		}
		w := newWriterEnv(t, plan.String())
		h := w.v.e.authed(w.v.e.login(t))
		var items []map[string]string
		for i, st := range w.v.e.s.featDossier(supTeam).Milestones[0].Steps {
			items = append(items, map[string]string{"key": st.Key, "check": cases[i].check, "cwd": cases[i].dir})
		}
		ab, _ := json.Marshal(map[string]any{"session": supTeam, "items": items})
		if r := w.v.e.do(t, "POST", "/api/term/feature/approve", string(ab), h); r.StatusCode != 200 {
			bad = append(bad, fmt.Sprintf("the owner's approve answered %d", r.StatusCode))
		}
		_, show := w.fdoc("", "show")
		outside := t.TempDir()
		for _, c := range cases {
			i := strings.Index(show, "] "+c.step+" @")
			j := strings.Index(show[max(i, 0):], "runAs: ")
			if i < 0 || j < 0 {
				bad = append(bad, c.step+": no runAs served after approval")
				continue
			}
			runAs := show[i+j+len("runAs: "):]
			if k := strings.Index(runAs, "\n  ["); k >= 0 { // up to the next step
				runAs = runAs[:k]
			}
			if k := strings.Index(runAs, "\n## "); k >= 0 {
				runAs = runAs[:k]
			}
			runAs = strings.TrimRight(runAs, "\n")
			cmd := exec.Command("bash", "-c", runAs)
			cmd.Dir = outside
			out, _ := cmd.CombinedOutput()
			lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			last := lines[len(lines)-1]
			if !strings.HasPrefix(last, c.want+" rfe-attempt=") {
				bad = append(bad, fmt.Sprintf("%s: last line %q, want %s", c.step, last, c.want))
			}
		}
		if _, err := os.Stat(filepath.Join(outside, "outside.txt")); err == nil {
			bad = append(bad, "Cd fails: the check ran outside its folder")
		}
		scenario(t, "W5 runAs: cd failure runs nothing (97), multi-command and exit report their status, a spoofed marker isn't last", &bad)
	})
}

// TestVMAgentManagerTools drives the manager's typed vm_mcp tools (create/update_feature_plan,
// propose_requirement_change, report_feature_progress) over the real routes: they use the same record and revisions as
// feature-doc, a wrong thread or a second create writes nothing, and FEATURE.md never changes.
func TestVMAgentManagerTools(t *testing.T) {
	w := newWriterEnv(t, "")
	dir := filepath.Dir(w.v.planFile())
	writeFile(t, filepath.Join(dir, "FEATURE.md"), "# Outcomes\n- [ ] Production is healthy\n")
	// tool runs one vm_mcp call and returns its output ("OK ..." or "REFUSED ...").
	tool := func(code string, args ...string) string {
		c := exec.Command("uv", append([]string{"run", "--quiet", "--with", "mcp>=2.3,<3", "python", "-c",
			"import sys,vm_mcp as m\ntry:\n print('OK', " + code + ")\nexcept m.PanelError as e:\n print('REFUSED', e)"}, args...)...)
		c.Dir = filepath.Join(w.vm, "mcp")
		c.Env = append(os.Environ(), "PANEL_AGENT_URL="+w.url, "PANEL_AGENT_TOKEN_FILE="+w.tok)
		b, _ := c.CombinedOutput()
		return strings.TrimSpace(string(b))
	}
	var bad []string
	thread := w.v.e.s.featDossier(supTeam).Thread
	if thread == "" {
		t.Fatal("precondition: the fixture's session has a managing thread")
	}
	plan := "## Ship\n- [ ] Deploy @Builder\n"
	if out := tool("m.create_feature_plan(sys.argv[1], sys.argv[2], sys.argv[3])", supTeam, "someone-else", plan); !strings.HasPrefix(out, "REFUSED") {
		bad = append(bad, "create under another thread: "+out)
	}
	if b, _ := os.ReadFile(w.v.planFile()); len(b) != 0 {
		bad = append(bad, "a refused create wrote PLAN.md")
	}
	if out := tool("m.create_feature_plan(sys.argv[1], sys.argv[2], sys.argv[3])", supTeam, thread, plan); !strings.HasPrefix(out, "OK PLAN.md written") {
		bad = append(bad, "create: "+out)
	}
	if b, _ := os.ReadFile(w.v.planFile()); string(b) != plan {
		bad = append(bad, fmt.Sprintf("PLAN.md after create: %q", b))
	}
	if out := tool("m.create_feature_plan(sys.argv[1], sys.argv[2], 'x')", supTeam, thread); !strings.Contains(out, "update_feature_plan") {
		bad = append(bad, "a second create: "+out)
	}
	rev := w.rev(t, "plan")
	replan := "## Ship\n- [ ] Deploy @Builder\n- [ ] Smoke test @Tester\n"
	if out := tool("m.update_feature_plan(sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4])", supTeam, thread, replan, rev); !strings.HasPrefix(out, "OK") {
		bad = append(bad, "update: "+out)
	}
	if out := tool("m.update_feature_plan(sys.argv[1], sys.argv[2], 'stale', sys.argv[3])", supTeam, thread, rev); !strings.Contains(out, "409") {
		bad = append(bad, "a stale update: "+out)
	}
	if b, _ := os.ReadFile(w.v.planFile()); string(b) != replan {
		bad = append(bad, fmt.Sprintf("PLAN.md after the stale update: %q", b))
	}
	if out := tool("m.get_feature_plan(sys.argv[1])", supTeam); !strings.Contains(out, "Smoke test") || !strings.Contains(out, "rev "+w.rev(t, "plan")) {
		bad = append(bad, "get_feature_plan: "+out)
	}
	if out := tool("m.get_feature_requirements(sys.argv[1])", supTeam); !strings.Contains(out, "open outcomes: Production is healthy") {
		bad = append(bad, "get_feature_requirements: "+out)
	}
	tool("m.propose_requirement_change(sys.argv[1], sys.argv[2], 'add a rollback outcome', 'deploys can fail')", supTeam, thread)
	tool("m.report_feature_progress(sys.argv[1], 'blocker', 'staging is down')", supTeam)
	logb, _ := os.ReadFile(filepath.Join(dir, "LOG.md"))
	for _, want := range []string{"REQUIREMENT PROPOSAL (" + thread + "): add a rollback outcome · why: deploys can fail", "BLOCKER: staging is down"} {
		if !strings.Contains(string(logb), want) {
			bad = append(bad, "LOG.md lacks "+want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "FEATURE.md")); string(b) != "# Outcomes\n- [ ] Production is healthy\n" {
		bad = append(bad, "FEATURE.md changed")
	}
	if st := w.v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State == "done" {
		bad = append(bad, "a progress/evidence log line verified a step")
	}
	scenario(t, "W6 the manager's typed tools write the same record under its revisions and scope", &bad)
}
