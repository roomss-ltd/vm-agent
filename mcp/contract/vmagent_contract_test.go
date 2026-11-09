package main

// The vm-agent side of the panel's contract. run.sh copies this file (it lives in vm-agent: mcp/contract/) into a
// checkout of the control-panel repo and runs it there. The panel's real agent API runs on an isolated home with fake
// zellij sockets and a fake Hermes; the real vm_mcp tools and bin/jarvis drive it (mcp/contract_panel.py).

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestVMAgentContract runs the vm-agent driver (mcp/contract_panel.py) against the panel's agent API, then checks from
// the Go side: the owner verifies the steps and accepts the outcomes so that vm_mcp's done is accepted; the lead's
// instructions are LEAD.md plus the owner's record block; threads, notes and the supervisor survive a panel restart and
// fleet pings go out once and only for material events; and a worker's bin/jarvis report reaches its lead. It is
// skipped unless VM_AGENT_DIR points at a vm-agent checkout.
func TestVMAgentContract(t *testing.T) {
	vm := os.Getenv("VM_AGENT_DIR")
	if vm == "" {
		t.Skip("VM_AGENT_DIR: the vm-agent checkout to test")
	}
	v := newSupEnv(t, "")
	v.e.s.scout.load() // the real server's scoutLoop does this; thread_open(team=) marks the takeover there
	home := v.e.s.cfg.home
	tok := filepath.Join(home, ".config", "control-panel", "agent-token")
	writeFile(t, tok, "contract-token")
	ts := httptest.NewServer(v.e.s.agentHandler("contract-token"))
	t.Cleanup(ts.Close)

	cmd := exec.Command("uv", "run", "--quiet", "--with", "mcp>=2.3,<3", "python", "contract_panel.py")
	cmd.Dir = filepath.Join(vm, "mcp")
	cmd.Env = append(os.Environ(), "PANEL_AGENT_URL="+ts.URL, "PANEL_AGENT_TOKEN_FILE="+tok,
		"CONTRACT_HOME="+home, "CONTRACT_SOCK="+v.e.s.sys.sockDir, "CONTRACT_THREADS="+v.a.threadsFile)
	var plog strings.Builder // the panel's log: shows which step of the team+plan update failed
	log.SetOutput(io.MultiWriter(os.Stderr, &plog))
	out, err := cmd.CombinedOutput()
	log.SetOutput(os.Stderr)
	t.Logf("driver:\n%s", out)
	if l := plog.String(); !strings.Contains(l, "thread fifth-manager: save:") || strings.Contains(l, "thread fifth-manager: plan intent") ||
		strings.Contains(l, "thread fifth-manager: plan write") {
		t.Errorf("SCENARIO FAILED: A3b the injected failure didn't hit the final threads.json save after the journal and PLAN.md writes; panel log:\n%s", l)
	} else {
		t.Log("SCENARIO ok: A3b the failure hit the final save, after the journal and PLAN.md writes")
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3 {
		t.Errorf("driver: panel gaps (above); the rest goes on")
	} else if err != nil {
		t.Fatalf("driver: %v", err)
	}
	m := regexp.MustCompile(`(?m)^TID=(\S+)$`).FindSubmatch(out)
	if m == nil {
		t.Fatal("no thread id from the driver")
	}
	// the owner verifies both steps on the page (session login, the plan's rev) and accepts the outcomes; then vm_mcp's done is accepted
	h := v.e.authed(v.e.login(t))
	vsess := "Owner-Feature"
	var g4 []string
	for _, st := range v.e.s.featDossier(vsess).Milestones[0].Steps {
		vb, _ := json.Marshal(map[string]string{"session": vsess, "key": st.Key, "rev": v.e.s.featDossier(vsess).Rev})
		if r := v.e.do(t, "POST", "/api/term/feature/verify", string(vb), h); r.StatusCode != 200 {
			g4 = append(g4, fmt.Sprintf("owner verify %q answered %d", st.Text, r.StatusCode))
		}
	}
	if why := vmAcceptOutcomes(t, v, vsess); why != "" {
		g4 = append(g4, why)
	}
	dc := exec.Command("uv", "run", "--quiet", "--with", "mcp>=2.3,<3", "python", "-c",
		"import sys,vm_mcp as m\nprint(m.thread_update(sys.argv[1], status='done', note='verified by the owner'))", string(m[1]))
	dc.Dir = filepath.Join(vm, "mcp")
	dc.Env = append(os.Environ(), "PANEL_AGENT_URL="+ts.URL, "PANEL_AGENT_TOKEN_FILE="+tok)
	if b, err := dc.CombinedOutput(); err != nil || !strings.Contains(string(b), "[done") {
		g4 = append(g4, fmt.Sprintf("vm_mcp done after the owner's verification: %v %s", err, b))
	}
	if len(g4) > 0 {
		t.Errorf("SCENARIO FAILED: G4 owner verification then done through vm_mcp: %s", strings.Join(g4, "; "))
	} else {
		t.Log("SCENARIO ok: G4 owner-verified steps and accepted outcomes, then done through vm_mcp")
	}
	// every lead turn: this checkout's LEAD.md, then the record block naming the owner's folder (startup/resume context)
	v.a.mu.Lock()
	v.a.leadFile = filepath.Join(vm, "hermes", "LEAD.md")
	ins := v.a.instructions(string(m[1]))
	v.a.mu.Unlock()
	rec := tilde(filepath.Join(home, "jarvis-files", "features", "Owner-Feature"))
	for _, want := range []string{"single source of truth", "team: the zellij session `Owner-Feature`", "[Feature record]", rec,
		"- M1 Owner's milestone [done] Owner's own step @Builder"} {
		if !strings.Contains(ins, want) {
			t.Fatalf("lead instructions lack %q:\n%s", want, ins)
		}
	}
	if strings.Contains(ins, "{id}") || strings.Contains(ins, "{team}") {
		t.Fatal("an unfilled placeholder in LEAD.md")
	}
	t.Log("SCENARIO ok: G1 lead instructions = this LEAD.md + the owner's record block")
	// restart across the boundary: fleet pings' cursor set, an agent of the bound team working, then the panel restarts
	// (threads, notes and supervisor reloaded from disk) and the agent is gone while it was down
	run := func(name string, args ...string) string {
		c := exec.Command(name, args...)
		c.Dir = filepath.Join(vm, "mcp")
		c.Env = append(os.Environ(), "PANEL_AGENT_URL="+ts.URL, "PANEL_AGENT_TOKEN_FILE="+tok)
		if name != "uv" { // fleet_pings reads ~ (token, cursor); uv keeps its cache under the real HOME
			c.Env = append(c.Env, "HOME="+home)
		}
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, b)
		}
		return string(b)
	}
	pings := func() string { return run("python3", filepath.Join(vm, "hermes", "scripts", "fleet_pings.py")) }
	pings()
	now := time.Now()
	look := func(panes ...TPane) {
		now = now.Add(31 * time.Second)
		v.e.s.observeTeams(now, Terminals{Sessions: []TSession{{Name: "Owner-Feature", Tabs: []TTab{{ID: 1, Name: "Builder", Panes: panes}}}}}, CI{}, Resources{})
	}
	look(v.agent("working", now.Unix()))
	v.a.mu.Lock()
	v.a.threads = nil
	v.a.loadThreads()
	th := *v.a.thread(string(m[1]))
	v.a.mu.Unlock()
	v.e.s.notes = newNotifier(v.e.s.notes.file)
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	look()
	look()
	v.e.s.raise(now, &th, true, "contract:material", "warn", "an approval has waited 3 minutes", "Builder asks to run the migration")
	v.e.s.raise(now, &th, false, "contract:lead-only", "info", "routine supervision only", "")
	ls := run("uv", "run", "--quiet", "--with", "mcp>=2.3,<3", "python", "-c", "import vm_mcp as m; print(m.thread_list(True)); print(m.recent_events())")
	if !strings.Contains(ls, string(m[1])+` "Owner feature manager" [done] team Owner-Feature`) || !strings.Contains(ls, "[exited/warn]") ||
		!strings.Contains(ls, "pane:Owner-Feature:3") {
		t.Fatalf("after the restart, thread_list/recent_events:\n%s", ls)
	}
	p := pings()
	if !strings.Contains(p, "an approval has waited 3 minutes [thread:"+string(m[1])+"]") || strings.Contains(p, "routine supervision") ||
		strings.Contains(p, "Owner-Feature:3") {
		t.Fatalf("fleet pings (owner: material only, a managed team's panes go to its lead):\n%s", p)
	}
	if p2 := pings(); p2 != "" {
		t.Fatalf("fleet pings repeated: %s", p2)
	}
	t.Log("SCENARIO ok: G2 restart (threads, notes, supervisor reloaded; exit while down) and fleet pings (material only, once)")
	t.Logf("after restart:\n%s\nowner pinged:\n%s", ls, p)
	// the worker's bin/jarvis report is a turn in that thread's lead conversation, tagged with its pane
	tid := string(m[1])
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var said []string
		for _, g := range v.turns("thread-" + tid) {
			said = append(said, g["message"].(string))
		}
		j := strings.Join(said, "\n---\n")
		if strings.Contains(j, "Owner-Feature:3") && strings.Contains(j, "step one done") {
			t.Log("SCENARIO ok: G3 the worker's report reached its lead")
			t.Logf("lead heard: %s", j[strings.Index(j, "Owner-Feature:3")-40:][:200])
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the worker's report never reached thread %s's lead; its turns:\n%s", tid, j)
		}
	}
}

// TestVMAgentInterruptedCommit simulates a crash at each point of a team+plan update (journal written, PLAN.md
// written, threads.json committed), then a restart. Afterwards the canonical PLAN.md and the binding (on disk and
// through vm_mcp) must agree: the whole update, or none of it. The crash is staged with the panel's own journal
// record (planEdit).
func TestVMAgentInterruptedCommit(t *testing.T) {
	vm := os.Getenv("VM_AGENT_DIR")
	if vm == "" {
		t.Skip("VM_AGENT_DIR: the vm-agent checkout to test")
	}
	v := newSupEnv(t, "")
	v.e.s.scout.load()
	home := v.e.s.cfg.home
	tok := filepath.Join(home, ".config", "control-panel", "agent-token")
	writeFile(t, tok, "contract-token")
	ts := httptest.NewServer(v.e.s.agentHandler("contract-token"))
	t.Cleanup(ts.Close)
	py := func(code string) string {
		c := exec.Command("uv", "run", "--quiet", "--with", "mcp>=2.3,<3", "python", "-c", "import vm_mcp as m\n"+code)
		c.Dir = filepath.Join(vm, "mcp")
		c.Env = append(os.Environ(), "PANEL_AGENT_URL="+ts.URL, "PANEL_AGENT_TOKEN_FILE="+tok)
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("vm_mcp: %v\n%s", err, b)
		}
		return string(b)
	}
	restart := func() {
		v.a.mu.Lock()
		v.a.threads = nil
		v.a.loadThreads()
		v.a.mu.Unlock()
	}
	onDisk := func(id string) string {
		var ts []thread
		b, _ := os.ReadFile(v.a.threadsFile)
		json.Unmarshal(b, &ts)
		for _, x := range ts {
			if x.ID == id {
				return x.Team
			}
		}
		return ""
	}
	const old, moved = "- [ ] Only step\n", "- [~] Only step\n"
	for i, crash := range []string{"after the journal", "after PLAN.md", "after threads.json"} {
		sess := fmt.Sprintf("Crash-%d", i)
		os.WriteFile(filepath.Join(v.e.s.sys.sockDir, sess), nil, 0o600)
		plan := filepath.Join(home, "jarvis-files", "features", sess, "PLAN.md")
		writeFile(t, plan, old)
		id := regexp.MustCompile(`opened thread (\S+)`).FindStringSubmatch(py(fmt.Sprintf("print(m.thread_open('Crash %d', 'b'))", i)))[1]
		v.a.mu.Lock()
		th := v.a.thread(id)
		e := planEdit{Path: plan, Old: []byte(old), New: []byte(moved), ID: id, Updated: th.Updated + 1}
		v.a.mu.Unlock()
		whole := false
		switch crash {
		case "after PLAN.md":
			os.WriteFile(plan, []byte(moved), 0o600)
		case "after threads.json": // a real update commits; the crash leaves its journal behind
			py(fmt.Sprintf("print(m.thread_update(%q, team=%q, plan='- [~] Only step'))", id, sess))
			v.a.mu.Lock()
			e.Updated = v.a.thread(id).Updated
			v.a.mu.Unlock()
			whole = true
		}
		jb, _ := json.Marshal(e)
		os.WriteFile(v.a.txnFile(), jb, 0o600)
		restart()
		b, _ := os.ReadFile(plan)
		ls := py("print(m.thread_list())")
		bound := strings.Contains(ls, id+` "Crash`) && strings.Contains(ls, "team "+sess)
		want, team := old, id
		if whole {
			want, team = moved, sess
		}
		if string(b) != want || onDisk(id) != team || bound != whole || txnLeft(v.a.txnFile()) {
			t.Errorf("SCENARIO FAILED: I%d crash %s: PLAN.md %q (want %q), team on disk %q (want %q), bound per vm_mcp %v, journal left %v\n%s",
				i+1, crash, b, want, onDisk(id), team, bound, txnLeft(v.a.txnFile()), ls)
			continue
		}
		t.Logf("SCENARIO ok: I%d crash %s, then restart: %s", i+1, crash, map[bool]string{true: "the whole update stands", false: "none of it"}[whole])
	}
}

// txnLeft reports whether the update journal file at p still exists.
func txnLeft(p string) bool { _, err := os.Stat(p); return err == nil }
