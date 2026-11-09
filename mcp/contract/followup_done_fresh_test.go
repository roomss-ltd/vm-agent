package main

// Tests that a thread's "done" stays current. When an accepted plan or outcome change leaves the completion
// unsupported, the panel reopens the thread to working. The historical completion evidence and audit stay, and
// reopening authorizes nothing. The tests assert what the user sees (GET /agent/threads through vm_mcp's thread_list,
// and the thread's status), not an internal hash. A write that lands after the done commit counts as a later edit (see
// TestVMAgentFollowup); these tests cover what follows.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVMAgentDoneFreshness checks that a later todo step, a new step, a changed outcome set or a hand edit of PLAN.md
// reopens a done thread without losing earlier evidence or implying any approval.
func TestVMAgentDoneFreshness(t *testing.T) {
	// status returns the feature thread's status.
	status := func(w *writerEnv) string {
		w.v.a.mu.Lock()
		defer w.v.a.mu.Unlock()
		return w.v.a.thread(supTeam).Status
	}
	// reopened records a failure unless what the lead and the owner read says working, not done.
	reopened := func(w *writerEnv, bad *[]string, what string) {
		out := w.py("m.thread_list(True)")
		if !strings.Contains(out, "[working") || strings.Contains(out, "[done") {
			*bad = append(*bad, what+": thread_list shows "+out)
		}
		if st := status(w); st != "working" {
			*bad = append(*bad, what+": status "+st)
		}
	}
	// setup builds a feature whose steps are verified and whose outcomes are accepted, and marks it done.
	setup := func(t *testing.T, plan string) *writerEnv {
		w := newWriterEnv(t, plan)
		if c := w.ownerVerify(t, 0); c != 200 {
			t.Fatalf("precondition: owner verify %d", c)
		}
		if why := vmAcceptOutcomes(t, w.v, supTeam); why != "" {
			t.Fatal("precondition: " + why)
		}
		if ok, out := w.vmDone("demo-feature"); !ok {
			t.Fatalf("precondition: done %s", out)
		}
		if out := w.py("m.thread_list(True)"); !strings.Contains(out, "[done") {
			t.Fatalf("precondition: thread_list after done: %s", out)
		}
		return w
	}
	// write rewrites PLAN.md through f, at the current revision, as the cooperative writer would.
	write := func(t *testing.T, w *writerEnv, f func(string) string) {
		b, _ := os.ReadFile(w.v.planFile())
		if err := featWriteRev(w.v.planFile(), featRev(b), []byte(f(string(b)))); err != nil {
			t.Fatal(err)
		}
	}
	// logOf returns the feature's LOG.md.
	logOf := func(w *writerEnv) string {
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(w.v.planFile()), "LOG.md"))
		return string(b)
	}

	t.Run("F8a a step back to todo after done reopens the thread", func(t *testing.T) {
		var bad []string
		w := setup(t, "## Ship\n- [x] Check output\n")
		before := logOf(w)
		write(t, w, func(s string) string { return strings.Replace(s, "- [x]", "- [ ]", 1) })
		reopened(w, &bad, "a todo step after done")
		if !strings.HasPrefix(logOf(w), before) {
			bad = append(bad, "LOG.md history before the reopen was not kept")
		}
		scenario(t, "F8a later todo step: reopened to working, history kept", &bad)
	})

	t.Run("F8b a new step after done reopens; the verified step keeps its evidence; nothing gets approved", func(t *testing.T) {
		var bad []string
		w := setup(t, "## Ship\n- [x] Check output\n  - check: go test ./...\n  - cwd: /w\n")
		was := w.step(0)
		write(t, w, func(s string) string { return s + "- [ ] Rollback drill @Builder\n  - kind: mutation\n" })
		reopened(w, &bad, "a new step after done")
		if st := w.step(0); st.State != "done" || st.Label != was.Label {
			bad = append(bad, fmt.Sprintf("the earlier verified step lost its completion: %s/%q, was %s/%q", st.State, st.Label, was.State, was.Label))
		}
		if st := w.step(0); st.Approval != was.Approval {
			bad = append(bad, fmt.Sprintf("reopening changed the approval of the verified step: %+v -> %+v", was.Approval, st.Approval))
		}
		if st := w.v.e.s.featDossier(supTeam).Milestones[0].Steps[1]; st.Approval.Check || st.Approval.Kind || st.State == "done" {
			bad = append(bad, fmt.Sprintf("the new mutation step came out approved or done: %+v", st))
		}
		scenario(t, "F8b later new step: reopened; earlier verification kept; no approval implied", &bad)
	})

	t.Run("F8c a changed outcome set after done reopens", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [x] Check output\n  - covers: D1, D2\n") // ids + coverage: id-less outcomes block done
		fp := filepath.Join(filepath.Dir(w.v.planFile()), "FEATURE.md")
		writeFile(t, fp, "# Outcomes\n- [ ] D1: Healthy\n")
		if c := w.ownerVerify(t, 0); c != 200 {
			t.Fatalf("precondition: owner verify %d", c)
		}
		if c := w.v.e.do(t, "POST", "/api/term/feature/accept", `{"session":"demo-feature","rev":"`+w.v.e.s.featDossier(supTeam).Revs["feature"]+`"}`, w.v.e.authed(w.v.e.login(t))).StatusCode; c != 200 {
			t.Fatalf("precondition: accept %d", c)
		}
		if ok, out := w.vmDone("demo-feature"); !ok {
			t.Fatalf("precondition: done %s", out)
		}
		writeFile(t, fp, "# Outcomes\n- [ ] D1: Healthy\n- [ ] D2: Rollback tested\n")
		reopened(w, &bad, "an outcome added after done (acceptance void)")
		scenario(t, "F8c later outcome change: reopened to working", &bad)
	})

	t.Run("F8d a direct edit of PLAN.md (no writer) is caught on the next read", func(t *testing.T) {
		var bad []string
		w := setup(t, "## Ship\n- [x] Check output\n")
		writeFile(t, w.v.planFile(), "## Ship\n- [x] Check output\n- [ ] Edited by hand\n")
		reopened(w, &bad, "a direct edit after done")
		scenario(t, "F8d direct edit: reconciled to working on read", &bad)
	})
}
