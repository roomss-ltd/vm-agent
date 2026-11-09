package main

// Checks that thread_update's answer carries the canonical plan (the one in the feature record) and that it equals what
// thread_list shows. It is checked across the client: the raw agent API and vm_mcp's rendering of both.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestVMAgentUpdateAnswersWithRecordPlan asserts that POST /agent/thread/update answers with the record's plan (same
// steps and states as GET /agent/threads shows) and that vm_mcp renders the update and the list alike.
func TestVMAgentUpdateAnswersWithRecordPlan(t *testing.T) {
	var bad []string
	w := newWriterEnv(t, "## Ship\n- [~] Write docs @Builder\n- [x] Read logs\n- [ ] Ship it\n")
	req := func(method, path, body string) map[string]any {
		r, _ := http.NewRequest(method, w.url+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer contract-token")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	planJSON := func(th any) string {
		m, _ := th.(map[string]any)
		b, _ := json.Marshal(m["plan"])
		return string(b)
	}
	upd := req("POST", "/agent/thread/update", `{"id":"demo-feature","note":"looking"}`)
	var listed string
	if ts, ok := req("GET", "/agent/threads", "")["threads"].([]any); ok {
		for _, th := range ts {
			if th.(map[string]any)["id"] == "demo-feature" {
				listed = planJSON(th)
			}
		}
	}
	got := planJSON(upd["thread"])
	if got == "null" || got == "[]" || got != listed {
		bad = append(bad, fmt.Sprintf("thread_update answered plan %s; thread_list shows %s", got, listed))
	}
	var steps []map[string]string
	json.Unmarshal([]byte(got), &steps)
	want := []string{"doing", "claimed", "todo"} // as verified: [~] doing, [x] with no evidence is only claimed, [ ] todo
	for i, s := range steps {
		if i < len(want) && s["state"] != want[i] {
			bad = append(bad, fmt.Sprintf("step %d state %q, the record says %q", i, s["state"], want[i]))
		}
	}
	if len(steps) != len(want) {
		bad = append(bad, fmt.Sprintf("%d steps in the answer, the record has %d", len(steps), len(want)))
	}
	// the client: what vm_mcp renders for the update matches its rendering of the list
	re := regexp.MustCompile(`plan \d+ of \d+ done`)
	u, l := re.FindString(w.py("m.thread_update('demo-feature', note='looking')")), re.FindString(w.py("m.thread_list(True)"))
	if u == "" || u != l {
		bad = append(bad, fmt.Sprintf("vm_mcp: thread_update shows %q, thread_list shows %q", u, l))
	}
	scenario(t, "U1 thread_update answers with the record's canonical plan, equal to thread_list (raw API and vm_mcp)", &bad)
}
