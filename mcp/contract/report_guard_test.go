package main

// A feature report is a paid model run (claude -p), so it must start only on the owner's explicit action. Viewing the
// page doesn't start one, a report request without explicit:true is refused, and no agent route reaches it; with
// explicit:true it starts exactly one. A stand-in `claude` that logs every call and refuses -p is put first on THIS
// test process's PATH only, so no real model runs and the calls can be counted.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestVMAgentReportNeedsExplicitOwner counts the stand-in claude's model runs to check that a report starts only on
// the owner's explicit request.
func TestVMAgentReportNeedsExplicitOwner(t *testing.T) {
	var bad []string
	bin := t.TempDir()
	calls := filepath.Join(bin, "claude-calls.log")
	stand := "#!/bin/sh\n# stand-in claude: log every call, refuse model runs\necho \"$*\" >> " + calls + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stand), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runs := func() int {
		b, _ := os.ReadFile(calls)
		return strings.Count(string(b), "-p ")
	}
	w := newWriterEnv(t, "## Ship\n- [ ] Step @Builder\n")
	w.v.look(w.v.agent("waiting", w.v.now.Unix())) // the session in the terminal listing: a report needs it
	waitIdle(t, w.v.a, supTeam)
	h := w.v.e.authed(w.v.e.login(t))
	busy := func() bool {
		featBusy.Lock()
		defer featBusy.Unlock()
		return featBusy.m[supTeam]
	}
	audits := func() (n int) {
		for _, e := range w.v.e.s.audit.list() {
			if e.Event == "feature-report" {
				n++
			}
		}
		return
	}
	a0 := audits()
	for i := 0; i < 3; i++ { // the owner opening the feature page, and the agent reading the record
		w.v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h)
		w.get(t, "/agent/feature?session="+supTeam)
	}
	if busy() || audits() != a0 || runs() != 0 {
		bad = append(bad, fmt.Sprintf("viewing the page or reading the record started a report (%d model runs)", runs()))
	}
	for _, body := range []string{`{"session":"demo-feature"}`, `{"session":"demo-feature","explicit":false}`} {
		if r := w.v.e.do(t, "POST", "/api/term/feature/report", body, h); r.StatusCode != 400 {
			bad = append(bad, "a report without explicit:true answered "+r.Status)
		}
		for end := time.Now().Add(2 * time.Second); busy() && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		}
	}
	agent := httptest.NewServer(w.v.e.s.agentHandler("agent-token"))
	defer agent.Close()
	for _, u := range []string{agent.URL + "/api/term/feature/report", agent.URL + "/agent/feature/report", w.v.e.srv.URL + "/api/term/feature/report"} {
		req, _ := http.NewRequest("POST", u, strings.NewReader(`{"session":"demo-feature","explicit":true}`))
		req.Header.Set("Authorization", "Bearer agent-token")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode < 400 {
				bad = append(bad, u+" started a report for the agent bearer")
			}
		}
	}
	if busy() || audits() != a0 || runs() != 0 {
		bad = append(bad, fmt.Sprintf("a refused request started a report (%d model runs)", runs()))
	}
	// the owner's explicit action: exactly one run (the stand-in refuses it, so the report itself fails: that's fine)
	if r := w.v.e.do(t, "POST", "/api/term/feature/report", `{"session":"demo-feature","explicit":true}`, h); r.StatusCode != 200 {
		bad = append(bad, "the owner's explicit report answered "+r.Status)
	}
	for end := time.Now().Add(10 * time.Second); busy() && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
	}
	if n := runs(); n != 1 {
		bad = append(bad, fmt.Sprintf("the explicit report made %d model runs, want exactly 1", n))
	}
	scenario(t, "M1 the paid report: 0 model runs on view, without explicit:true or by the bearer; exactly 1 on the owner's explicit action", &bad)
}
