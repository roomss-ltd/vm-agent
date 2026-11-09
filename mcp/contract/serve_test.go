package main

// Not a regular test: a disposable panel for a smoke run against a real Hermes (mcp/contract/hermes-smoke.sh). It
// serves the panel's real agent API on an isolated home (fake zellij sockets), with an owner feature "Smoke-Feature"
// and a thread bound to it. It is skipped unless VM_AGENT_SERVE=<dir> is set; it then writes <dir>/panel.env
// (PANEL_AGENT_URL, PANEL_AGENT_TOKEN_FILE, SMOKE_THREAD, SMOKE_PLAN) and serves until <dir>/stop exists or 15 minutes
// pass. It never touches a live panel, its state or zellij.

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestVMAgentServe starts the disposable panel described above, then records the plan and thread state it ends with
// into <dir>/plan-after.md and <dir>/thread-after.txt for the smoke script to inspect.
func TestVMAgentServe(t *testing.T) {
	dir := os.Getenv("VM_AGENT_SERVE")
	if dir == "" {
		t.Skip("VM_AGENT_SERVE: where to write panel.env")
	}
	v := newSupEnv(t, "")
	v.e.s.scout.load()
	home := v.e.s.cfg.home
	const sess = "Smoke-Feature"
	os.WriteFile(filepath.Join(v.e.s.sys.sockDir, sess), nil, 0o600)
	writeFile(t, filepath.Join(featDir(home, sess), "PLAN.md"), "## Smoke\n- [ ] Say hello @Builder\n- [ ] Second step\n")
	v.a.mu.Lock()
	th := v.a.newThread("Smoke manager", "")
	th.Team, th.Status = sess, "working"
	v.a.saveThreads()
	v.a.mu.Unlock()
	tok := filepath.Join(dir, "agent-token")
	writeFile(t, tok, "smoke-token")
	ts := httptest.NewServer(v.e.s.agentHandler("smoke-token"))
	t.Cleanup(ts.Close)
	writeFile(t, filepath.Join(dir, "panel.env"), fmt.Sprintf("PANEL_AGENT_URL=%s\nPANEL_AGENT_TOKEN_FILE=%s\nSMOKE_THREAD=%s\nSMOKE_PLAN=%s\n",
		ts.URL, tok, th.ID, filepath.Join(featDir(home, sess), "PLAN.md")))
	for end := time.Now().Add(15 * time.Minute); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
			break
		}
	}
	b, _ := os.ReadFile(filepath.Join(featDir(home, sess), "PLAN.md"))
	writeFile(t, filepath.Join(dir, "plan-after.md"), string(b))
	v.a.mu.Lock()
	writeFile(t, filepath.Join(dir, "thread-after.txt"), fmt.Sprintf("team=%s status=%s plan=%v\n", th.Team, th.Status, th.Plan))
	v.a.mu.Unlock()
}
