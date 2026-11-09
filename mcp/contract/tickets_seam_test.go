//go:build seam

package main

// This file is built only with the seam build tag. run.sh adds -tags seam when the panel defines the
// featTicketBeforeEffect hook, so the rest of the suite still compiles on panels without it.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestVMAgentTicketEffectRecheck checks that a ticket reply's agent is checked again at the moment of the effect, under
// the record lock. The featTicketBeforeEffect hook replaces the agent between the reply's checks and the owner route's
// effect; the reply must then answer 409, change neither the record nor the supervisor, and record no ticket event.
// A clean reply records a ticket event naming the step, tab, checked sid, rev and option.
//
// Known limitation (from reading the code, not testable here): the recheck reads the panel's last terminal sample
// (termTracker.list, guarded by its own mutex; the sampler doesn't take featPlanMu), so an agent replaced after that
// sample, or between the recheck and the write by a sample that lands then, is not seen.
func TestVMAgentTicketEffectRecheck(t *testing.T) {
	// hook installs f as the seam that runs between a reply's checks and its effect (nil removes it).
	hook := func(w *writerEnv, f func()) {
		featTicketBeforeEffect = f
		w.v.e.s.sup.mu.Lock()   // published through the lock the reply takes (featTickets) before it reads the hook
		w.v.e.s.sup.mu.Unlock() //nolint:staticcheck
	}
	type tev struct{ Kind, Step, Tab, Sid, Rev, Text string }
	ticketEvents := func(w *writerEnv, t *testing.T) []tev {
		_, raw := w.get(t, "/agent/feature/events?session="+supTeam)
		var p struct{ Events []tev }
		json.Unmarshal(raw, &p)
		var out []tev
		for _, e := range p.Events {
			if e.Kind == "ticket" {
				out = append(out, e)
			}
		}
		return out
	}
	planBytes := func(w *writerEnv) string { b, _ := os.ReadFile(w.v.planFile()); return string(b) }
	// race replies to tk while swap replaces the tabs' agents at the effect, and returns what went wrong: the reply
	// must be a 409 "replaced" and must change neither PLAN.md, the supervisor, nor the ticket events.
	race := func(t *testing.T, w *writerEnv, tk ticket, option, text string, swap map[string]string) []string {
		var bad []string
		p0, s0 := planBytes(w), w.supBytes(t)
		hook(w, func() { w.setTabs(swap) })
		c, out := w.reply(t, tk, option, text, tk.Sid)
		hook(w, nil)
		if c != 409 || !strings.Contains(fmt.Sprint(out["error"]), "replaced") {
			bad = append(bad, fmt.Sprintf("%s with the agent replaced at the effect: %d %v, want 409", option, c, out))
		}
		if planBytes(w) != p0 {
			bad = append(bad, option+": PLAN.md changed on a 409")
		}
		if w.supBytes(t) != s0 {
			bad = append(bad, option+": the supervisor (approvals, holds, events) changed on a 409")
		}
		if len(ticketEvents(w, t)) != 0 {
			bad = append(bad, option+": a ticket event for a refused effect")
		}
		return bad
	}
	// clean replies to a fresh ticket of the given kind with the right agent sid and returns what went wrong: the
	// reply must succeed and record exactly one ticket event naming the step, tab, sid, rev, option and ticket id.
	clean := func(t *testing.T, w *writerEnv, kind, option, text, sid string) []string {
		var bad []string
		tk := w.tickets(t)[kind]
		if tk.Sid != sid {
			return []string{fmt.Sprintf("the fresh %s ticket names sid %q, want %q", kind, tk.Sid, sid)}
		}
		if c, out := w.reply(t, tk, option, text, sid); c != 200 {
			return []string{fmt.Sprintf("the clean %s: %d %v", option, c, out)}
		}
		evs := ticketEvents(w, t)
		if len(evs) != 1 || evs[0].Sid != sid || evs[0].Tab != tk.Tab || evs[0].Step != tk.Step || evs[0].Rev != tk.Rev || !strings.Contains(evs[0].Text, option) || !strings.Contains(evs[0].Text, tk.ID) {
			bad = append(bad, fmt.Sprintf("the %s receipt: %+v, want step %s tab %s sid %s rev %s with %s/%s", option, evs, tk.Step, tk.Tab, sid, tk.Rev, tk.ID, option))
		}
		return bad
	}
	// ask starts an env with Builder and Writer agents and an ask_owner question on Writer's step, and returns it with
	// the ask ticket.
	ask := func(t *testing.T) (*writerEnv, ticket) {
		w := newWriterEnv(t, ticketsPlan)
		d := w.v.e.s.featDossier(supTeam)
		w.setTabs(map[string]string{"Builder": "sid-b1", "Writer": "sid-w1"})
		w.py("m.ask_owner(sys.argv[1], sys.argv[2], sys.argv[3], 'eu or us?')", supTeam, d.Thread, d.Milestones[0].Steps[1].Key)
		return w, w.tickets(t)["ask"]
	}

	t.Run("TE1 answer: agent replaced at the effect -> 409, nothing written; clean answer -> receipt", func(t *testing.T) {
		w, tk := ask(t)
		bad := race(t, w, tk, "answer", "eu", map[string]string{"Builder": "sid-b1", "Writer": "sid-w2"})
		if w.step(1).State != "asking" {
			bad = append(bad, "the refused answer moved the step")
		}
		bad = append(bad, clean(t, w, "ask", "answer", "eu", "sid-w2")...)
		scenario(t, "TE1 answer: replaced at the effect 409 (PLAN.md, supervisor, events unchanged); the fresh answer records step/tab/sid/rev/option", &bad)
	})

	t.Run("TE2 approve: agent replaced at the effect -> 409, nothing approved; clean approve -> receipt", func(t *testing.T) {
		w, _ := ask(t)
		tk := w.tickets(t)["approve"]
		bad := race(t, w, tk, "approve", "", map[string]string{"Builder": "sid-b2", "Writer": "sid-w1"})
		if w.step(0).Approval.Check {
			bad = append(bad, "the refused approve approved the check")
		}
		bad = append(bad, clean(t, w, "approve", "approve", "", "sid-b2")...)
		scenario(t, "TE2 approve: replaced at the effect 409 (nothing approved, no event); the fresh approve records its receipt", &bad)
	})

	t.Run("TE3 release: agent replaced at the effect -> 409, still held; clean release -> receipt", func(t *testing.T) {
		w := newWriterEnv(t, "## Ship\n- [~] Change DNS @Builder\n  - kind: mutation\n")
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		w.v.look()
		w.v.look() // Builder exited mid-mutation: held
		w.setTabs(map[string]string{"Builder": "sid-b5"})
		tk := w.tickets(t)["hold"]
		if tk.Sid != "sid-b5" {
			t.Fatalf("precondition: a hold ticket naming the new agent: %+v", tk)
		}
		bad := race(t, w, tk, "release", "", map[string]string{"Builder": "sid-b6"})
		if w.step(0).Blocked == "" {
			bad = append(bad, "the refused release lifted the hold")
		}
		bad = append(bad, clean(t, w, "hold", "release", "", "sid-b6")...)
		scenario(t, "TE3 release: replaced at the effect 409 (still held, no event); the fresh release records its receipt", &bad)
	})
}
