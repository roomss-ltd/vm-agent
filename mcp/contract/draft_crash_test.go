package main

// Tests for promoting a draft (FEATURE.draft.md) to FEATURE.md when the promotion is interrupted at each stage.
// Promotion records its intent in <record>/.promote-txn; promoteHook injects a failure at a stage, and
// recoverPromoteTxn finishes or rolls back after a failure or a simulated crash. A failure or crash before the commit
// point must leave the OLD state, and one after it the NEW state. Either way the state is whole: FEATURE.md, the draft
// and the acceptance never split, and no completion or authorization outlives the outcomes it was for. The exact
// boundary isn't asserted beyond that: with no prior acceptance, FEATURE equal to the draft counts as committed.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// promoteStages are the points where a promotion is interrupted. A stage named after-* simulates the process dying
// there, leaving the files as they are; the others make that step fail with an error.
var promoteStages = []struct {
	stage string
	crash bool // after-*: the process dies there, files as they are
}{
	{"intent", false}, {"after-intent", true}, {"feature", false}, {"after-feature", true},
	{"commit", false}, {"after-commit", true}, {"draft", false},
}

// whole returns "old" or "new" when FEATURE.md, the draft and the acceptance all agree on one of the two states, else
// a description of what is mixed (or that a .promote-txn is left behind after recovery).
func (d *draftEnv) whole(old, draft string, oldAccepted bool) string {
	f, dr := d.read("FEATURE.md"), d.read("FEATURE.draft.md")
	_, err := os.Stat(d.path("FEATURE.draft.md"))
	acc := d.v.e.s.featDossier(supTeam).Accepted
	_, txn := os.Stat(d.path(".promote-txn"))
	switch {
	case txn == nil:
		return "a .promote-txn is left after recovery"
	case f == old && dr == draft && acc == oldAccepted:
		return "old"
	case f == draft && os.IsNotExist(err) && !acc:
		return "new"
	}
	return fmt.Sprintf("SPLIT feature=%q draft=%q (exists %v) accepted=%v", f, dr, err == nil, acc)
}

// promoteWithHook makes the promotion fail at stage (through promoteHook), calls the owner's promote route with the
// current revisions, and returns its HTTP status.
func promoteWithHook(t *testing.T, d *draftEnv, stage string) int {
	boom := errors.New("injected")
	promoteHook = func(s string) error {
		if s == stage {
			return boom
		}
		return nil
	}
	defer func() { promoteHook = nil }()
	rv := d.revs(t)
	return d.owner(t, "promote", map[string]string{"session": supTeam, "draftRev": rv["draft"], "featureRev": rv["feature"]})
}

// recoverAfter runs the promotion recovery the way the panel does after an interruption; after a crash it first
// reloads the supervisor state from disk, as a restart would.
func (d *draftEnv) recoverAfter(crash bool) {
	if crash { // a restart: the supervisor's memory from disk first, then recovery
		d.v.e.s.sup = newFeatSup(d.v.e.s.sup.file)
	}
	d.v.e.s.recoverPromoteTxn(d.dir)
}

// TestVMAgentDraftPromoteCrash checks that a promotion interrupted at any stage leaves a whole old or new state, with
// and without a prior acceptance, and that recovery keeps a newer draft written meanwhile.
func TestVMAgentDraftPromoteCrash(t *testing.T) {
	const next = "# Desired outcome\n- [ ] D1: Healthy\n- [ ] D2: Rollback tested\n"

	t.Run("P7 every stage, with a prior acceptance and a done feature: whole state; new state reopens and grants nothing", func(t *testing.T) {
		var bad []string
		for _, st := range promoteStages {
			d := newDraftEnv(t)
			if ok, out := d.vmDone("demo-feature"); !ok {
				t.Fatalf("precondition: done %s", out)
			}
			d.draft(t, next)
			old, appr := d.read("FEATURE.md"), fmt.Sprint(d.step(0).Approval)
			c := promoteWithHook(t, d, st.stage)
			d.recoverAfter(st.crash)
			got := d.whole(old, next, true)
			switch got {
			case "old":
				if c == 200 {
					bad = append(bad, st.stage+": 200 but the old state stands")
				}
			case "new":
				if out := d.py("m.thread_list(True)"); !strings.Contains(out, "[working") {
					bad = append(bad, st.stage+": new outcomes but the thread still shows done: "+out)
				}
				if ok, _ := d.vmDone("demo-feature"); ok {
					bad = append(bad, st.stage+": done accepted on outcomes the owner hasn't accepted")
				}
			default:
				bad = append(bad, st.stage+": "+got)
			}
			if fmt.Sprint(d.step(0).Approval) != appr {
				bad = append(bad, st.stage+": a step approval changed")
			}
			t.Logf("stage %s (crash %v): HTTP %d, state %s", st.stage, st.crash, c, got)
		}
		scenario(t, "P7 promote interrupted at each stage: whole state; new reopens; nothing approved", &bad)
	})

	t.Run("P8 every stage with no prior acceptance: whole state, never an acceptance from nowhere", func(t *testing.T) {
		var bad []string
		for _, st := range promoteStages {
			w := newWriterEnv(t, "## Ship\n- [x] Check output\n  - covers: D1, D2\n")
			d := &draftEnv{w, filepath.Dir(w.v.planFile())}
			writeFile(t, d.path("FEATURE.md"), "# Desired outcome\n- [ ] D1: Healthy\n")
			d.draft(t, next)
			old := d.read("FEATURE.md")
			c := promoteWithHook(t, d, st.stage)
			d.recoverAfter(st.crash)
			got := d.whole(old, next, false)
			if got != "old" && got != "new" {
				bad = append(bad, st.stage+": "+got)
			}
			if d.v.e.s.featDossier(supTeam).Accepted {
				bad = append(bad, st.stage+": accepted although the owner never accepted anything")
			}
			t.Logf("stage %s (crash %v): HTTP %d, state %s", st.stage, st.crash, c, got)
		}
		scenario(t, "P8 no prior acceptance: whole state at each stage, nothing accepted", &bad)
	})

	t.Run("P9 a newer draft written after a crash past the commit is kept by its rev", func(t *testing.T) {
		var bad []string
		d := newDraftEnv(t)
		d.draft(t, next)
		promoteWithHook(t, d, "after-commit")
		d.v.e.s.sup = newFeatSup(d.v.e.s.sup.file) // the restart, before recovery runs
		if _, err := os.Stat(d.path(".promote-txn")); err != nil {
			t.Fatal("precondition: the crash after the commit left no intent to recover")
		}
		// In-process the writer and promote share the record lock, so a newer draft can only meet an unsettled intent
		// on disk: an edit that lands before the restart's recovery (the panel recovers before it serves any write).
		newer := next + "- [ ] Docs published\n"
		writeFile(t, d.path("FEATURE.draft.md"), newer)
		wroteNewer := true
		d.v.e.s.recoverPromoteTxn(d.dir)
		if d.read("FEATURE.md") != next {
			bad = append(bad, fmt.Sprintf("FEATURE.md isn't the committed draft: %q", d.read("FEATURE.md")))
		}
		if wroteNewer && d.read("FEATURE.draft.md") != newer {
			bad = append(bad, "recovery deleted a newer draft (a different rev from the promoted one)")
		}
		if !wroteNewer {
			bad = append(bad, "precondition: the newer draft couldn't be written after the crash")
		}
		if d.v.e.s.featDossier(supTeam).Accepted {
			bad = append(bad, "the old acceptance survived the committed promotion")
		}
		scenario(t, "P9 recovery keeps a newer draft (rev differs); FEATURE = committed draft; acceptance cleared", &bad)
	})
}
