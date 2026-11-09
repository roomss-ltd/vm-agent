package main

// Shared helpers for the contract tests: reporting a scenario's outcome, running one vm_mcp call through the agent API,
// and having the owner accept a feature's desired outcomes.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// scenario logs a contract scenario's outcome: "SCENARIO ok" or "SCENARIO FAILED" with what broke.
func scenario(t *testing.T, name string, failures *[]string) {
	t.Helper()
	if len(*failures) == 0 {
		t.Log("SCENARIO ok: " + name)
		return
	}
	t.Errorf("SCENARIO FAILED: %s: %s", name, strings.Join(*failures, "; "))
}

// keys returns the keys of m, in no particular order.
func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// py makes one vm_mcp call through the agent API, as a lead would. code is a Python expression using the vm_mcp module
// as m and args as sys.argv[1:]; the result is "OK <result>" or "REFUSED <why>".
func (w *writerEnv) py(code string, args ...string) string {
	c := exec.Command("uv", append([]string{"run", "--quiet", "--with", "mcp>=2.3,<3", "python", "-c",
		"import sys,vm_mcp as m\ntry:\n print('OK', " + code + ")\nexcept m.PanelError as e:\n print('REFUSED', e)"}, args...)...)
	c.Dir = filepath.Join(w.vm, "mcp")
	c.Env = append(os.Environ(), "PANEL_AGENT_URL="+w.url, "PANEL_AGENT_TOKEN_FILE="+w.tok)
	b, _ := c.CombinedOutput()
	return strings.TrimSpace(string(b))
}

// vmAcceptOutcomes does what done requires of the desired outcomes. FEATURE.md must list an outcome with an id (D1 is
// written if it lists none, because id-less outcomes block new features), some step must cover each id (if none covers
// it, the owner adds "- covers: D1" to every step and re-attests the steps they had verified, as a coverage change
// requires), and the owner must accept the outcomes at their revision. It returns "" if they were accepted, else why not.
func vmAcceptOutcomes(t *testing.T, v *supEnv, session string) string {
	t.Helper()
	d := v.e.s.featDossier(session)
	if !strings.Contains(d.Feature, "- [ ] ") {
		writeFile(t, d.Paths["FEATURE.md"], d.Feature+"\n# Desired outcome\n- [ ] D1: The feature does what the plan says\n")
		d = v.e.s.featDossier(session)
	}
	covered := false
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			covered = covered || slices.Contains(st.Covers, "D1")
		}
	}
	h := v.e.authed(v.e.login(t))
	if !covered && strings.Contains(d.Feature, "D1:") {
		var attested []string
		for _, m := range d.Milestones {
			for _, st := range m.Steps {
				if st.Label == "owner verified" {
					attested = append(attested, st.Key)
				}
			}
		}
		text := regexp.MustCompile(`(?m)^(\s*)([-*] \[.\] .*)$`).ReplaceAllString(d.Plan, "$1$2\n$1  - covers: D1")
		b, _ := json.Marshal(map[string]string{"session": session, "doc": "plan", "text": text, "rev": d.Rev})
		if r := v.e.do(t, "POST", "/api/term/feature/doc", string(b), h); r.StatusCode != 200 {
			return fmt.Sprintf("the owner's covers: D1 edit answered %d", r.StatusCode)
		}
		for _, k := range attested {
			b, _ := json.Marshal(map[string]string{"session": session, "key": k, "rev": v.e.s.featDossier(session).Rev})
			if r := v.e.do(t, "POST", "/api/term/feature/verify", string(b), h); r.StatusCode != 200 {
				return fmt.Sprintf("the owner's re-attestation of %s after the coverage change answered %d", k, r.StatusCode)
			}
		}
		d = v.e.s.featDossier(session)
	}
	r := v.e.do(t, "POST", "/api/term/feature/accept", `{"session":"`+session+`","rev":"`+d.Revs["feature"]+`"}`, h)
	if r.StatusCode != 200 {
		return fmt.Sprintf("the owner's accept of the outcomes answered %d", r.StatusCode)
	}
	return ""
}
