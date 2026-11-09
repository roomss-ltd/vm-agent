package main

// Tests for owner tickets: the things the owner is asked to act on (an agent's question, a check to approve, a hold to
// release, a draft to promote or discard, outcomes to accept, a missed checkpoint). Tickets are derived from the record
// and the supervisor on each read (page.tickets on the owner's GET, tickets on the agent's GET /agent/feature). They
// are answered only by the owner's POST /api/term/feature/ticket/reply {session, ticket, option, text?, rev, sid?}: the
// option must be one the server offers now for that ticket, and the reply then does exactly what the matching owner
// route does.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// ticket is one owner ticket as the page and agent reads show it: its id and kind, the step and tab it concerns, the
// session id of the agent it expects, the revision it was derived at, and the options the server offers for it.
type ticket struct {
	ID, Kind, Step, Tab, Sid, Rev, Title string
	Options                              []struct {
		ID        string
		NeedsText bool
	}
}

// tickets reads the open tickets from the agent's GET /agent/feature, keyed by kind.
func (w *writerEnv) tickets(t *testing.T) map[string]ticket {
	_, raw := w.get(t, "/agent/feature?session="+supTeam)
	var r struct{ Tickets []ticket }
	json.Unmarshal(raw, &r)
	out := map[string]ticket{}
	for _, tk := range r.Tickets {
		out[tk.Kind] = tk
	}
	return out
}

// setTabs installs a terminal listing for supTeam with one Claude pane per tab; tabs maps a tab name to the session id
// of the agent in it.
func (w *writerEnv) setTabs(tabs map[string]string) {
	var ts []TTab
	id := 1
	for name, sid := range tabs {
		ts = append(ts, TTab{ID: id, Name: name, Panes: []TPane{{ID: 10 + id, Kind: "claude", State: "idle", Agent: &TAgent{PID: 50 + id, sid: sid}}}})
		id++
	}
	tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: ts}}}
	w.v.e.s.term.mu.Lock()
	w.v.e.s.term.last, w.v.e.s.term.list = tm, tm
	w.v.e.s.term.mu.Unlock()
}

// reply sends the owner's reply to ticket tk (option, optional text, and the sid of the agent it expects) and returns
// the status code and the answer.
func (w *writerEnv) reply(t *testing.T, tk ticket, option, text, sid string) (int, map[string]any) {
	return w.owner(t, "/api/term/feature/ticket/reply", map[string]any{"session": supTeam, "ticket": tk.ID, "option": option, "text": text, "rev": tk.Rev, "sid": sid})
}

// ticketsPlan is the plan most ticket tests start from: a step with a check for Builder to run and an open step for
// Writer.
const ticketsPlan = "## Ship\n- [ ] Run the suite @Builder\n  - check: go test ./...\n  - cwd: /tmp\n- [ ] Pick a region @Writer\n"

// TestVMAgentOwnerTickets covers the ticket read and reply rules: both reads derive the same tickets with server-named
// options; the agent bearer cannot reply, and forged routes, tickets, kinds and free text are refused with nothing
// done; a stale revision or a replaced agent gets a 409 with the current value; answer-wait leaves a step paused while
// answer resumes it; draft discard goes through the ticket like its route; and releasing a hold lifts only the hold.
func TestVMAgentOwnerTickets(t *testing.T) {
	// setup: an approve ticket (a check) on Builder, and an ask the lead raised through vm_mcp on Writer
	setup := func(t *testing.T) (*writerEnv, map[string]ticket) {
		w := newWriterEnv(t, ticketsPlan)
		d := w.v.e.s.featDossier(supTeam)
		w.setTabs(map[string]string{"Builder": "sid-b1", "Writer": "sid-w1"})
		if out := w.py("m.ask_owner(sys.argv[1], sys.argv[2], sys.argv[3], 'eu or us?', options='eu | us')", supTeam, d.Thread, d.Milestones[0].Steps[1].Key); !strings.HasPrefix(out, "OK asked") {
			t.Fatalf("ask_owner: %s", out)
		}
		tk := w.tickets(t)
		if tk["ask"].ID == "" || tk["approve"].ID == "" {
			t.Fatalf("precondition: an ask and an approve ticket: %+v", tk)
		}
		return w, tk
	}
	state := func(w *writerEnv) string { // everything a reply could change: PLAN.md, approvals, holds, answers
		d := w.v.e.s.featDossier(supTeam)
		b, _ := json.Marshal(d.Milestones)
		return d.Plan + string(b) + w.supBytes(nil)
	}

	t.Run("O1 read: one derivation on both reads, server-named options, vm_mcp shows them, reads change nothing", func(t *testing.T) {
		var bad []string
		w, tk := setup(t)
		ask, ap := tk["ask"], tk["approve"]
		if ask.Sid != "sid-w1" || ask.Tab != "Writer" || len(ask.Options) != 2 || ask.Options[0].ID != "answer" || !ask.Options[0].NeedsText || ask.Options[1].ID != "answer-wait" {
			bad = append(bad, fmt.Sprintf("the ask ticket: %+v", ask))
		}
		if ap.Sid != "sid-b1" || len(ap.Options) != 1 || ap.Options[0].ID != "approve" {
			bad = append(bad, fmt.Sprintf("the approve ticket: %+v", ap))
		}
		if _, ok := tk["outcomes"]; ok {
			bad = append(bad, "an outcomes ticket while steps aren't verified")
		}
		before := state(w)
		w.setTabs(map[string]string{"Builder": "sid-b1", "Writer": "sid-w1"})
		pg := map[string]ticket{}
		r := w.v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", w.v.e.authed(w.v.e.login(t)))
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		var f struct{ Page struct{ Tickets []ticket } }
		json.Unmarshal(raw, &f)
		for _, x := range f.Page.Tickets {
			pg[x.Kind] = x
		}
		a, _ := json.Marshal(pg)
		b, _ := json.Marshal(w.tickets(t))
		if string(a) != string(b) {
			bad = append(bad, fmt.Sprintf("page.tickets %s differs from the agent read %s", a, b))
		}
		if out := w.py("m.feature_record(sys.argv[1])", supTeam); !strings.Contains(out, "OPEN OWNER TICKETS") || !strings.Contains(out, ask.ID) || !strings.Contains(out, "eu or us?") {
			bad = append(bad, "vm_mcp feature_record: "+out)
		}
		if state(w) != before {
			bad = append(bad, "a read changed the record or the supervisor")
		}
		scenario(t, "O1 tickets: the same derivation on the owner page and the agent read; options named by the server; vm_mcp lists them; reads write nothing", &bad)
	})

	t.Run("O2 authority: the bearer can't reply; only offered options; forged routes, tickets and free text refused, nothing done", func(t *testing.T) {
		var bad []string
		w, tk := setup(t)
		ask, ap := tk["ask"], tk["approve"]
		before := state(w)
		for _, path := range []string{"/api/term/feature/ticket/reply", "/agent/feature/ticket/reply"} {
			if c, out := w.postAgent(t, path, map[string]any{"session": supTeam, "ticket": ap.ID, "option": "approve", "rev": ap.Rev, "sid": ap.Sid}); c == 200 {
				bad = append(bad, fmt.Sprintf("the agent bearer replied via %s: %v", path, out))
			}
		}
		for _, x := range []struct {
			tk                ticket
			option, text, why string
			want              int
		}{
			{ap, "accept", "", "an option of another ticket kind", 400},
			{ap, "release", "", "release on an approve ticket", 400},
			{ap, "/api/term/feature/promote", "", "a client-named route", 400},
			{ap, "verify", "", "an owner route no ticket offers", 400},
			{ask, "approve", "", "approve on an ask ticket", 400},
			{ask, "", "eu please", "free text with no option", 400},
			{ask, "answer", "  ", "answer with no text", 400},
			{ticket{ID: "approve:" + ask.Step, Rev: ask.Rev}, "approve", "", "a forged approve ticket on the ask step (no check)", 409},
			{ticket{ID: "verify:" + ap.Step, Rev: ap.Rev}, "verify", "", "a forged ticket kind", 409},
		} {
			sid := x.tk.Sid
			if c, out := w.reply(t, x.tk, x.option, x.text, sid); c != x.want {
				bad = append(bad, fmt.Sprintf("%s: %d %v, want %d", x.why, c, out, x.want))
			} else if x.want == 400 && x.option == "" && out["error"] == nil {
				bad = append(bad, "free text was refused without saying why")
			}
		}
		if state(w) != before {
			bad = append(bad, "a refused reply changed something (approved, answered or released)")
		}
		if c, _ := w.owner(t, "/api/term/feature/ticket/reply", map[string]any{"session": supTeam}); c != 400 {
			bad = append(bad, fmt.Sprintf("no ticket: %d", c))
		}
		scenario(t, "O2 bearer denied (owner route and no agent route); options scoped to the ticket; forged route/ticket/kind, free text with no option, empty answer: refused, explicit, nothing done", &bad)
	})

	t.Run("O3 stale: a changed revision or a replaced agent is 409 with the current value; the fresh reply works", func(t *testing.T) {
		var bad []string
		w, tk := setup(t)
		ap := tk["approve"]
		before := state(w)
		if c, out := w.reply(t, ticket{ID: ap.ID, Rev: "stale"}, "approve", "", ap.Sid); c != 409 || out["rev"] != ap.Rev {
			bad = append(bad, fmt.Sprintf("a stale rev: %d %v, want 409 {rev: %s}", c, out, ap.Rev))
		}
		w.setTabs(map[string]string{"Builder": "sid-b2", "Writer": "sid-w1"}) // Builder's agent replaced
		if c, out := w.reply(t, ap, "approve", "", "sid-b1"); c != 409 || out["sid"] != "sid-b2" {
			bad = append(bad, fmt.Sprintf("the replaced agent's sid: %d %v, want 409 {sid: sid-b2}", c, out))
		}
		if c, out := w.reply(t, ap, "approve", "", ""); c != 409 {
			bad = append(bad, fmt.Sprintf("no sid while an agent is on the tab: %d %v, want 409", c, out))
		}
		if state(w) != before {
			bad = append(bad, "a stale reply changed something")
		}
		ap = w.tickets(t)["approve"]
		if c, out := w.reply(t, ap, "approve", "", "sid-b2"); c != 200 {
			bad = append(bad, fmt.Sprintf("the fresh approve: %d %v", c, out))
		}
		if !w.step(0).Approval.Check {
			bad = append(bad, "approved through the ticket, but the check isn't approved as the route would")
		}
		if c, out := w.reply(t, ap, "approve", "", "sid-b2"); c != 409 {
			bad = append(bad, fmt.Sprintf("the closed ticket again: %d %v", c, out))
		}
		scenario(t, "O3 stale rev 409 {rev}, replaced or missing sid 409 {sid}, nothing done; the fresh reply acts as the route; a closed ticket is 409", &bad)
	})

	t.Run("O4 answer-wait leaves the work paused; answer resumes; other lanes are untouched", func(t *testing.T) {
		var bad []string
		w, tk := setup(t)
		ask := tk["ask"]
		builder := func() string { b, _ := json.Marshal(w.step(0)); return string(b) }
		b0 := builder()
		if c, out := w.reply(t, ask, "answer-wait", "not yet: wait for legal", ask.Sid); c != 200 {
			t.Fatalf("answer-wait: %d %v", c, out)
		}
		st := w.step(1)
		if st.State == "doing" || st.State == "asking" || !strings.Contains(st.Answer, "wait for legal") {
			bad = append(bad, fmt.Sprintf("after answer-wait the step is %q (answer %q): want paused (todo) with the answer recorded", st.State, st.Answer))
		}
		if _, open := w.tickets(t)["ask"]; open {
			bad = append(bad, "the answered ask is still a ticket")
		}
		if builder() != b0 || w.tickets(t)["approve"].ID == "" {
			bad = append(bad, "answering Writer's ask changed Builder's step or closed its approve ticket")
		}
		if typed := w.v.typed(); len(typed) != 0 {
			bad = append(bad, fmt.Sprintf("a reply typed into a pane: %v", typed))
		}
		// a second ask, answered to go on
		d := w.v.e.s.featDossier(supTeam)
		w.py("m.ask_owner(sys.argv[1], sys.argv[2], sys.argv[3], 'and the bucket?')", supTeam, d.Thread, d.Milestones[0].Steps[1].Key)
		ask = w.tickets(t)["ask"]
		if c, out := w.reply(t, ask, "answer", "eu-west-1", ask.Sid); c != 200 {
			bad = append(bad, fmt.Sprintf("answer: %d %v", c, out))
		} else if st := w.step(1); st.State != "doing" {
			bad = append(bad, fmt.Sprintf("after answer the step is %q, want doing", st.State))
		}
		if builder() != b0 {
			bad = append(bad, "the second answer changed Builder's step")
		}
		scenario(t, "O4 answer-wait: answer kept, step paused (not doing), ticket closed; answer: resumes; Builder's lane untouched; nothing typed", &bad)
	})

	t.Run("O5 draft promote/discard and the hold release go through the ticket like their routes; outcomes only when all steps are verified", func(t *testing.T) {
		var bad []string
		w, _ := setup(t)
		d := w.v.e.s.featDossier(supTeam)
		feature0 := d.Feature
		if out := w.py("m.create_draft_requirements(sys.argv[1], sys.argv[2], '- [ ] exports work\\n')", supTeam, d.Thread); !strings.HasPrefix(out, "OK") {
			t.Fatalf("draft: %s", out)
		}
		dr := w.tickets(t)["draft"]
		if dr.ID == "" || len(dr.Options) != 2 {
			t.Fatalf("no draft ticket: %+v", dr)
		}
		if c, out := w.reply(t, ticket{ID: dr.ID, Rev: "stale"}, "discard", "", ""); c != 409 {
			bad = append(bad, fmt.Sprintf("discard at a stale draft rev: %d %v", c, out))
		}
		if c, out := w.reply(t, dr, "discard", "", ""); c != 200 {
			bad = append(bad, fmt.Sprintf("discard: %d %v", c, out))
		}
		if d := w.v.e.s.featDossier(supTeam); d.Draft != "" || d.Feature != feature0 {
			bad = append(bad, "discard through the ticket didn't delete the draft, or changed FEATURE.md")
		}
		if c, _ := w.reply(t, dr, "discard", "", ""); c != 409 {
			bad = append(bad, "the discarded draft's ticket replied again")
		}
		if _, ok := w.tickets(t)["outcomes"]; ok {
			bad = append(bad, "an outcomes ticket while no step is verified")
		}
		if c, _ := w.reply(t, ticket{ID: "outcomes:feature", Rev: d.Revs["feature"]}, "accept", "", ""); c != 409 {
			bad = append(bad, "accepting outcomes through a ticket that isn't offered")
		}
		scenario(t, "O5 draft discard via the ticket = the route (stale 409, FEATURE.md kept, closed after); no outcomes ticket or accept before every step is verified", &bad)
	})

	t.Run("O6 a hold from an uncertain exit: release via the ticket lifts it and nothing else", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [~] Change DNS @Builder\n  - kind: mutation\n- [ ] Write docs @Docs\n")
		w.v.look(w.v.agent("working", w.v.now.Unix()))
		w.v.look()
		w.v.look() // Builder exited mid-mutation: held
		hd := w.tickets(t)["hold"]
		if hd.ID != "hold:builder" || len(hd.Options) != 1 || hd.Options[0].ID != "release" {
			t.Fatalf("precondition: a hold ticket: %+v", w.tickets(t))
		}
		if c, out := w.reply(t, hd, "approve", "", ""); c != 400 || w.step(0).Blocked == "" {
			bad = append(bad, fmt.Sprintf("approve on a hold: %d %v", c, out))
		}
		docs := w.step(1)
		if c, out := w.reply(t, hd, "release", "", ""); c != 200 {
			bad = append(bad, fmt.Sprintf("release: %d %v", c, out))
		}
		if st := w.step(0); st.Blocked != "" || st.State == "done" || st.Label == "owner verified" {
			bad = append(bad, fmt.Sprintf("after release: %+v (want unheld and nothing more: not done, not verified)", st))
		}
		if a, b := fmt.Sprint(docs), fmt.Sprint(w.step(1)); a != b {
			bad = append(bad, "releasing Builder changed Docs' step")
		}
		if _, open := w.tickets(t)["hold"]; open {
			bad = append(bad, "the released hold is still a ticket")
		}
		scenario(t, "O6 hold ticket from a real exit; release via the ticket lifts the hold only (no done, no verify, other step untouched)", &bad)
	})
}

// TestVMAgentOwnerTicketsPositive covers the ticket replies that change the record: promoting a draft, accepting
// outcomes (offered only once every step is verified), and the missed-checkpoint ticket, which offers no options.
func TestVMAgentOwnerTicketsPositive(t *testing.T) {
	t.Run("O7 draft promote via the ticket: FEATURE.md becomes the draft, the draft goes, outcomes need accepting again", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, ticketsPlan)
		d := w.v.e.s.featDossier(supTeam)
		draft := "# Export\n\n## Desired outcomes\n- [ ] CSV opens in a spreadsheet\n"
		if out := w.py("m.create_draft_requirements(sys.argv[1], sys.argv[2], sys.argv[3])", supTeam, d.Thread, draft); !strings.HasPrefix(out, "OK") {
			t.Fatalf("draft: %s", out)
		}
		dr := w.tickets(t)["draft"]
		if c, out := w.reply(t, dr, "promote", "", ""); c != 200 {
			t.Fatalf("promote: %d %v", c, out)
		}
		nd := w.v.e.s.featDossier(supTeam)
		if strings.TrimSpace(nd.Feature) != strings.TrimSpace(draft) || nd.Draft != "" || nd.Accepted {
			bad = append(bad, fmt.Sprintf("after promote: feature %q draft %q accepted %v", nd.Feature, nd.Draft, nd.Accepted))
		}
		if _, open := w.tickets(t)["draft"]; open {
			bad = append(bad, "the promoted draft is still a ticket")
		}
		if c, _ := w.reply(t, dr, "promote", "", ""); c != 409 {
			bad = append(bad, "the promoted draft's ticket replied again")
		}
		scenario(t, "O7 promote via the ticket = the route: FEATURE.md is the draft, no draft left, not accepted, ticket closed", &bad)
	})

	t.Run("O8 outcomes accept via the ticket, offered only once every step is verified", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [x] Export CSV @Builder\n  - covers: D1\n- [x] Write docs @Docs\n  - covers: D1\n")
		d := w.v.e.s.featDossier(supTeam)
		writeFile(t, d.Paths["FEATURE.md"], d.Feature+"\n## Desired outcomes\n- [ ] D1: CSV opens in a spreadsheet\n") // the outcome has an id, D1
		if _, ok := w.tickets(t)["outcomes"]; ok {
			bad = append(bad, "an outcomes ticket before the owner verified the steps")
		}
		for i := 0; i < 2; i++ {
			if c := w.ownerVerify(t, i); c != 200 {
				t.Fatalf("precondition: verify %d: %d", i, c)
			}
		}
		oc := w.tickets(t)["outcomes"]
		if oc.ID != "outcomes:feature" || len(oc.Options) != 1 || oc.Options[0].ID != "accept" {
			t.Fatalf("no outcomes ticket with every step verified: %+v", w.tickets(t))
		}
		if c, out := w.reply(t, ticket{ID: oc.ID, Rev: "stale"}, "accept", "", ""); c != 409 || w.v.e.s.featDossier(supTeam).Accepted {
			bad = append(bad, fmt.Sprintf("accept at a stale FEATURE.md rev: %d %v", c, out))
		}
		if c, out := w.reply(t, oc, "accept", "", ""); c != 200 || !w.v.e.s.featDossier(supTeam).Accepted {
			bad = append(bad, fmt.Sprintf("accept: %d %v", c, out))
		}
		if _, open := w.tickets(t)["outcomes"]; open {
			bad = append(bad, "the accepted outcomes are still a ticket")
		}
		scenario(t, "O8 no outcomes ticket until every step is verified; then accept via the ticket = the route (stale 409), ticket closed", &bad)
	})

	t.Run("O9 a missed checkpoint is a ticket with no options; a reply is refused, nothing done", func(t *testing.T) {
		var bad []string
		w := newWriterEnv(t, "## Ship\n- [ ] Export CSV @Builder\n  - due: 2020-01-01 09:00\n- [ ] Write docs @Docs\n  - due: 2099-01-01 09:00\n")
		tk := w.tickets(t)
		ms := tk["missed"]
		if ms.ID != "missed:"+w.step(0).Key || len(ms.Options) != 0 {
			t.Fatalf("missed ticket: %+v", tk)
		}
		n := 0
		for _, x := range []string{"missed:" + w.step(1).Key} {
			_, raw := w.get(t, "/agent/feature?session="+supTeam)
			n = strings.Count(string(raw), `"`+x+`"`)
		}
		if n != 0 {
			bad = append(bad, "a step due in 2099 is a missed ticket")
		}
		before := w.supBytes(t)
		for _, opt := range []string{"", "approve", "release", "answer"} {
			if c, _ := w.reply(t, ms, opt, "ok", ""); c != 400 {
				bad = append(bad, fmt.Sprintf("reply %q to a missed ticket: %d, want 400", opt, c))
			}
		}
		if w.supBytes(t) != before || w.step(0).State != "todo" {
			bad = append(bad, "a refused reply to the missed ticket changed something")
		}
		scenario(t, "O9 missed checkpoint: a ticket with no options (not for a future due); every reply 400, nothing done", &bad)
	})
}
