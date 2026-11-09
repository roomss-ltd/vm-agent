package main

// Compatibility tests for a deployed vm-agent checkout against this panel: every agent route the checkout calls is
// served, and its own vm_mcp tools work end to end. LIVE_VM_AGENT_DIR names that checkout (run.sh passes it through);
// without it these tests skip. They are self-contained (they use only the panel's own newTestServer and
// threadAssistant), so they can also run against an already deployed panel as a positive control.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveReport logs a scenario's outcome like scenario does, without depending on helpers_test.go.
func liveReport(t *testing.T, name string, bad []string) {
	t.Helper()
	if len(bad) == 0 {
		t.Log("SCENARIO ok: " + name)
		return
	}
	t.Errorf("SCENARIO FAILED: %s: %s", name, strings.Join(bad, "; "))
}

// liveEnv is a running agent API (url) with its token file (tok) and the id of one working thread (id).
type liveEnv struct{ url, tok, id string }

// newLiveEnv serves the panel's agent API on a test server with one working thread and a token file for the client.
func newLiveEnv(t *testing.T) liveEnv {
	e := newTestServer(t)
	a, _ := threadAssistant(t)
	e.s.ai = a
	a.mu.Lock()
	th := a.newThread("Live companion check", "")
	th.Status = "working"
	a.saveThreads()
	id := th.ID
	a.mu.Unlock()
	os.WriteFile(filepath.Join(e.s.sys.sockDir, id), nil, 0o600)
	tok := filepath.Join(t.TempDir(), "agent-token")
	os.WriteFile(tok, []byte("live-token\n"), 0o600)
	ts := httptest.NewServer(e.s.agentHandler("live-token"))
	t.Cleanup(ts.Close)
	return liveEnv{ts.URL, tok, id}
}

// TestVMAgentLiveCompanion runs the deployed vm-agent checkout named by LIVE_VM_AGENT_DIR against this panel: every
// agent route it calls must be served, and its vm_mcp tools must work.
func TestVMAgentLiveCompanion(t *testing.T) {
	live := os.Getenv("LIVE_VM_AGENT_DIR")
	if live == "" {
		t.Skip("LIVE_VM_AGENT_DIR: the deployed vm-agent checkout (set it to also check the deployed checkout)")
	}
	t.Run("C1 every route the live companion calls is served (no mux 404/405)", func(t *testing.T) {
		var bad []string
		v := newLiveEnv(t)
		cl := &http.Client{Timeout: 25 * time.Second}
		for _, r := range []struct{ method, path, body string }{
			{"GET", "/agent/snapshot?view=term", ""},
			{"GET", "/agent/pane/screen?session=" + v.id + "&pane=3", ""},
			{"GET", "/agent/pane/chat?session=" + v.id + "&pane=3&n=5", ""},
			{"GET", "/agent/pane/files?session=" + v.id + "&pane=3", ""},
			{"POST", "/agent/pane/input", `{"session":"no-such-session","pane":1,"text":"x"}`},
			{"POST", "/agent/term/new", `{}`},
			{"POST", "/agent/term/close", `{}`},
			{"GET", "/agent/notes", ""},
			{"GET", "/agent/threads", ""},
			{"POST", "/agent/thread/open", `{}`},
			{"POST", "/agent/thread/send", `{}`},
			{"POST", "/agent/thread/update", `{}`},
		} {
			req, _ := http.NewRequest(r.method, v.url+r.path, strings.NewReader(r.body))
			req.Header.Set("Authorization", "Bearer live-token")
			resp, err := cl.Do(req)
			if err != nil {
				bad = append(bad, r.path+": "+err.Error())
				continue
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 405 || (resp.StatusCode == 404 && strings.Contains(string(b), "404 page not found")) {
				bad = append(bad, r.method+" "+r.path+" not served")
			}
		}
		liveReport(t, "C1 live companion routes all served, pane/files included", bad)
	})
	t.Run("C2 the live companion's own tools against this panel", func(t *testing.T) {
		var bad []string
		v := newLiveEnv(t)
		// tool runs one vm_mcp call from the live checkout and returns its output ("OK ..." or "REFUSED ...").
		tool := func(code string) string {
			c := exec.Command("uv", "run", "--quiet", "--with", "mcp>=2.3,<3", "python", "-c",
				"import sys,vm_mcp as m\ntry:\n print('OK', "+code+")\nexcept m.PanelError as e:\n print('REFUSED', e)")
			c.Dir = filepath.Join(live, "mcp")
			c.Env = append(os.Environ(), "PANEL_AGENT_URL="+v.url, "PANEL_AGENT_TOKEN_FILE="+v.tok)
			b, _ := c.CombinedOutput()
			return strings.TrimSpace(string(b))
		}
		for _, x := range []struct{ code, want string }{
			{"m.thread_update('" + v.id + "', note='live companion check')", "OK " + v.id},
			{"m.thread_list(True)", "OK " + v.id},
			{"m.recent_events()", "OK"},
			{"m.pane_files('" + v.id + "', 3)", ""}, // no transcript in the fixture: its own 404 JSON is fine, a missing route isn't
		} {
			out := tool(x.code)
			if strings.Contains(out, "404: Not Found") || strings.Contains(out, "405") || strings.Contains(out, "Traceback") || (x.want != "" && !strings.HasPrefix(out, x.want)) {
				if len(out) > 300 {
					out = out[:300]
				}
				bad = append(bad, x.code+": "+out)
			}
		}
		if out := tool("m.thread_list(True)"); !strings.Contains(out, "live companion check") {
			bad = append(bad, "the live client's note didn't land: "+out)
		}
		liveReport(t, "C2 live companion tools (thread_update, thread_list, recent_events, pane_files)", bad)
	})
}
