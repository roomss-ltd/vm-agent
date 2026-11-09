package main

// Tests that a done recorded without this panel's done gate (no Completed mark) stays done and is labelled historical
// and unverified (dossier.doneHistorical, not doneStale, which is for a gated done that stopped holding). It isn't
// reopened, enrolled as completed or pinged, whether read, seen by a supervisor look or restarted, and nothing about it
// reads as evidence: its ticked step stays "claimed". A done that did pass this gate still reopens when its plan
// changes (see TestVMAgentDoneFreshness).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestVMAgentLegacyDone checks that a done thread with no completion mark is left alone and labelled historical, on a
// plain read, after a supervisor look and after a restart.
func TestVMAgentLegacyDone(t *testing.T) {
	var bad []string
	w := newWriterEnv(t, "## Ship\n- [x] Migrated by hand\n- [ ] Never done\n")
	w.v.a.mu.Lock() // as an earlier panel left it: done, with no completion mark from this panel's gate
	w.v.a.thread(supTeam).Status = "done"
	w.v.a.saveThreads()
	w.v.a.mu.Unlock()
	notes0 := len(w.v.notes("thread"))
	// marks reads the dossier's doneHistorical and doneStale labels and the first milestone's steps from the agent route.
	marks := func() (historical, stale string, steps []map[string]any) {
		_, b := w.get(t, "/agent/feature?session="+supTeam)
		var r struct {
			Dossier struct {
				DoneHistorical string `json:"doneHistorical"`
				DoneStale      string `json:"doneStale"`
				Milestones     []struct {
					Steps []map[string]any `json:"steps"`
				} `json:"milestones"`
			} `json:"dossier"`
		}
		json.Unmarshal(b, &r)
		if len(r.Dossier.Milestones) > 0 {
			steps = r.Dossier.Milestones[0].Steps
		}
		return r.Dossier.DoneHistorical, r.Dossier.DoneStale, steps
	}
	// check records a failure for each way the historical done has been disturbed, labelling it with when.
	check := func(when string) {
		if out := w.py("m.thread_list(True)"); !strings.Contains(out, "[done") {
			bad = append(bad, when+": a historical done was flipped: "+out)
		}
		hist, stale, steps := marks()
		if hist == "" {
			bad = append(bad, when+": the record doesn't mark the done historical/unverified (doneHistorical empty)")
		}
		if stale != "" {
			bad = append(bad, when+": a historical done reported as a gated done gone stale (doneStale): "+stale)
		}
		if len(steps) < 1 || steps[0]["state"] != "claimed" || steps[0]["label"] == "owner verified" || steps[0]["label"] == "check ran (receipt)" {
			bad = append(bad, fmt.Sprintf("%s: the historical done lends its ticked step false evidence: %v", when, steps))
		}
		var sup struct {
			Completed map[string]int64 `json:"completed"`
		}
		sb, _ := os.ReadFile(w.v.e.s.sup.file)
		json.Unmarshal(sb, &sup)
		if sup.Completed[supTeam] != 0 {
			bad = append(bad, when+": the historical done was enrolled as completed by this panel's gate")
		}
		if n := len(w.v.notes("thread")); n != notes0 {
			bad = append(bad, when+": an unsolicited ping (thread note) for a historical done")
		}
		if strings.Contains(w.v.e.s.featDossier(supTeam).Log, "reopened") {
			bad = append(bad, when+": LOG.md says reopened")
		}
	}
	check("on read")
	w.v.look(w.v.agent("waiting", w.v.now.Unix())) // a supervisor look
	check("after a supervisor look")
	w.v.e.s.sup = newFeatSup(w.v.e.s.sup.file) // a restart
	// publish it the way the server reads it: its handlers take a.mu before touching s.sup, and the requests come from a
	// client subprocess, which gives the race detector no ordering of its own
	w.v.a.mu.Lock()
	w.v.a.mu.Unlock() //nolint:staticcheck // an empty critical section: a happens-before edge, nothing else
	check("after a restart")
	scenario(t, "L1 historical done: stays done, labelled historical/unverified, no false evidence, no reopen/enrollment/ping", &bad)
}
