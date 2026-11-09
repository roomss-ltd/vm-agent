package main

// Tests for owner comments posted on the feature page and delivered to the managing lead thread through the outbox.
// The lead is a fake Hermes (rfeHermes, see delivery_contract_test.go) that can lose replies and hide its history.
// A comment whose delivery outcome is unknown is never sent again and never reported as delivered. If the feature is
// rebound to another thread before delivery, the comment is held and never retargeted. A comment's client id keeps
// deduplicating retries even after its event has been compacted out of the event log.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// feature binds the lead thread to the test feature: it marks the thread as managing rfeLead, creates the matching
// zellij socket and writes a one-step PLAN.md.
func (v *rfeEnv) feature(t *testing.T) {
	v.a.mu.Lock()
	v.a.thread(rfeLead).Team = rfeLead
	v.a.saveThreads()
	v.a.mu.Unlock()
	os.WriteFile(filepath.Join(v.e.s.sys.sockDir, rfeLead), nil, 0o600)
	writeFile(t, filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", rfeLead, "PLAN.md"), "## A\n- [ ] Export CSV @Builder\n")
}

// owner sends a request to the panel as the logged-in owner and returns the status and the decoded JSON body.
func (v *rfeEnv) owner(t *testing.T, method, path string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	if body == nil {
		b = nil
	}
	r := v.e.do(t, method, path, string(b), v.e.authed(v.e.login(t)))
	defer r.Body.Close()
	var out map[string]any
	json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out
}

// msgState returns the delivery state the panel reports for the owner comment with the given client id.
func (v *rfeEnv) msgState(t *testing.T, id string) map[string]any {
	_, out := v.owner(t, "GET", "/api/term/feature/message?session="+rfeLead+"&id="+id, nil)
	return out
}

// count returns how many messages in a Hermes session contain text: the copies Hermes kept in its history, or, when
// tries is true, the copies it was asked to take.
func (f *rfeHermes) count(session, text string, tries bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.hist
	if tries {
		src = f.tries
	}
	n := 0
	for _, m := range src[session] {
		if strings.Contains(m, text) {
			n++
		}
	}
	return n
}

// TestVMAgentOwnerMessageDelivery checks the two ways an owner comment must not be mishandled on the way to the lead:
// resent or called delivered when the outcome is unknown, and retargeted when the feature is rebound.
func TestVMAgentOwnerMessageDelivery(t *testing.T) {
	t.Run("MS3 a lost reply with the history unreadable: unknown, never resent, never delivered; delivered once the history shows it", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.feature(t)
		text := "ms3-owner-comment: hold the release"
		v.f.mu.Lock()
		v.f.mode, v.f.histDown = "drop", true // Hermes keeps it, the answer is lost, and the history can't say
		v.f.mu.Unlock()
		if c, out := v.owner(t, "POST", "/api/term/feature/message", map[string]any{"session": rfeLead, "clientId": "c-ms3", "thread": rfeLead, "text": text, "anchor": map[string]string{"kind": "feature"}}); c != 200 {
			t.Fatalf("post: %d %v", c, out)
		}
		if !v.settle(func() bool { return v.f.count("thread-"+rfeLead, text, true) > 0 }) {
			t.Fatalf("precondition: never dispatched")
		}
		v.f.set("ok")
		for range 10 {
			v.tick(10 * time.Minute)
		}
		st := v.msgState(t, "c-ms3")
		if st["state"] != "unknown" {
			bad = append(bad, fmt.Sprintf("state with the outcome unknown: %v, want unknown", st))
		}
		if n := v.f.count("thread-"+rfeLead, text, true); n != 1 {
			bad = append(bad, fmt.Sprintf("sent %d times while its outcome was unknown, want 1 (never resent)", n))
		}
		v.f.mu.Lock()
		v.f.histDown = false
		v.f.mu.Unlock()
		if !v.settle(func() bool { return v.msgState(t, "c-ms3")["state"] == "delivered" }) {
			bad = append(bad, fmt.Sprintf("once the history shows it kept: %v, want delivered", v.msgState(t, "c-ms3")))
		}
		if k, n := v.f.count("thread-"+rfeLead, text, false), v.f.count("thread-"+rfeLead, text, true); k != 1 || n != 1 {
			bad = append(bad, fmt.Sprintf("copies kept %d, sent %d: want exactly one of each", k, n))
		}
		scenario(t, "MS3 lost reply + unreadable history: state unknown, sent once, not delivered; history back: delivered, one copy", &bad)
	})

	t.Run("MS4 the feature rebound to another thread before delivery: held for the owner, never retargeted or sent to either", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.feature(t)
		text := "ms4-owner-comment: use the eu bucket"
		v.f.set("503") // the lead is unreachable: nothing delivered yet
		if c, out := v.owner(t, "POST", "/api/term/feature/message", map[string]any{"session": rfeLead, "clientId": "c-ms4", "thread": rfeLead, "text": text, "anchor": map[string]string{"kind": "feature"}}); c != 200 {
			t.Fatalf("post: %d %v", c, out)
		}
		if !v.settle(func() bool { return v.f.count("thread-"+rfeLead, text, true) > 0 }) {
			t.Fatalf("precondition: never dispatched")
		}
		v.a.mu.Lock() // FIXTURE: a second lead thread (thread_open would start a real session)
		other := v.a.newThread("Other lead", "")
		other.Status, other.Created = "working", 1
		otherID := other.ID
		v.a.saveThreads()
		v.a.mu.Unlock()
		// the rebind through the agent route: the old lead moves to another live session, the other takes the feature
		os.WriteFile(filepath.Join(v.e.s.sys.sockDir, "parked-session"), nil, 0o600)
		if c, out := v.agentPost(t, "/agent/thread/update", map[string]any{"id": rfeLead, "team": "parked-session"}); c != 200 {
			t.Fatalf("unbind: %d %v", c, out)
		}
		if c, out := v.agentPost(t, "/agent/thread/update", map[string]any{"id": otherID, "team": rfeLead}); c != 200 {
			t.Fatalf("rebind: %d %v", c, out)
		}
		v.f.set("ok")
		for range 10 {
			v.tick(10 * time.Minute)
		}
		st := v.msgState(t, "c-ms4")
		if st["state"] != "held" || st["thread"] != rfeLead {
			bad = append(bad, fmt.Sprintf("after the rebind: %v, want held, still addressed to %s", st, rfeLead))
		}
		if n := v.f.count("thread-"+otherID, text, true); n != 0 {
			bad = append(bad, fmt.Sprintf("retargeted: the new lead %s was sent it %d time(s)", otherID, n))
		}
		if k := v.f.count("thread-"+rfeLead, text, false); k != 0 {
			bad = append(bad, fmt.Sprintf("the state says held, but the old lead (no longer managing the feature) kept %d copy(ies)", k))
		}
		if c, out := v.owner(t, "POST", "/api/term/feature/message", map[string]any{"session": rfeLead, "clientId": "c-ms4", "thread": rfeLead, "text": text, "anchor": map[string]string{"kind": "feature"}}); c != 409 || out["thread"] != otherID {
			bad = append(bad, fmt.Sprintf("a retry to the old lead: %d %v, want 409 {thread: %s}", c, out, otherID))
		}
		scenario(t, "MS4 rebind before delivery: held and still addressed to the old lead, not sent to the new lead nor kept by the old one, retry 409 naming the new manager", &bad)
	})
}

// agentPost POSTs a JSON body to the agent handler with the agent token and returns the status and the decoded body.
func (v *rfeEnv) agentPost(t *testing.T, path string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	ts := httptest.NewServer(v.e.s.agentHandler("rfe-token"))
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer rfe-token")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var out map[string]any
	json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out
}

// TestVMAgentOwnerMessageCompactedDedupe checks that a comment's client id still deduplicates a retry, and still binds
// the original payload, after the comment's event was compacted out of the event log: the dedupe must not live only in
// that log.
func TestVMAgentOwnerMessageCompactedDedupe(t *testing.T) {
	var bad []string
	w := newWriterEnv(t, "## Ship\n- [ ] Export CSV @Builder\n")
	d := w.v.e.s.featDossier(supTeam)
	msg := map[string]any{"session": supTeam, "clientId": "c-ms5", "thread": d.Thread, "text": "ms5-owner-comment: wait for legal", "anchor": map[string]string{"kind": "feature"}}
	if c, out := w.owner(t, "/api/term/feature/message", msg); c != 200 || out["event"] != "ev-1" {
		t.Fatalf("post: %d %v", c, out)
	}
	seedEvents(w, 5000, func(int) map[string]any { // FIXTURE: 5000 later events, then one routed event compacts ev-1
		return map[string]any{"kind": "assign", "step": d.Milestones[0].Steps[0].Key, "tab": "Builder", "thread": d.Thread}
	})
	if c, out := w.postAgent(t, "/agent/feature/assign", map[string]any{"session": supTeam, "step": d.Milestones[0].Steps[0].Key, "tab": "Builder", "thread": d.Thread}); c != 200 {
		t.Fatalf("assign: %d %v", c, out)
	}
	if _, p := w.events(t, "&limit=1"); len(p.Events) == 0 || p.Events[0].ID == "ev-1" {
		t.Fatalf("precondition: the comment's event ev-1 compacted: %+v", p.Events)
	}
	c, out := w.owner(t, "/api/term/feature/message", msg) // the page retries the same comment
	n := 0
	for _, x := range w.v.notes("") {
		if x.Ref == "msg/"+supTeam+"/c-ms5" {
			n++
		}
	}
	if n != 1 {
		bad = append(bad, fmt.Sprintf("the same clientId after its event was compacted: %d %v and %d outbox notes, want 1 (a second lead turn)", c, out, n))
	}
	other := map[string]any{}
	for k, v := range msg {
		other[k] = v
	}
	other["text"] = "a different comment"
	if c, out := w.owner(t, "/api/term/feature/message", other); c != 409 {
		bad = append(bad, fmt.Sprintf("the same clientId with another payload after compaction: %d %v, want 409", c, out))
	}
	scenario(t, "MS5 a comment's clientId still dedupes (and binds its payload) after its event is compacted (5000-event FIXTURE seed)", &bad)
}

// TestVMAgentOwnerMessageRebindEdges checks that ownership of the feature is rechecked on every queued retry and
// after a restart, and that an in-flight send whose outcome is unknown when the feature is rebound stays honestly
// uncertain (never "held", as if it had been stopped).
func TestVMAgentOwnerMessageRebindEdges(t *testing.T) {
	rebind := func(t *testing.T, v *rfeEnv) string {
		v.a.mu.Lock() // FIXTURE: a second lead thread (thread_open would start a real session)
		other := v.a.newThread("Other lead", "")
		other.Status, other.Created = "working", 1
		id := other.ID
		v.a.saveThreads()
		v.a.mu.Unlock()
		os.WriteFile(filepath.Join(v.e.s.sys.sockDir, "parked-session"), nil, 0o600)
		if c, out := v.agentPost(t, "/agent/thread/update", map[string]any{"id": rfeLead, "team": "parked-session"}); c != 200 {
			t.Fatalf("unbind: %d %v", c, out)
		}
		if c, out := v.agentPost(t, "/agent/thread/update", map[string]any{"id": id, "team": rfeLead}); c != 200 {
			t.Fatalf("rebind: %d %v", c, out)
		}
		return id
	}
	post := func(t *testing.T, v *rfeEnv, cid, text string) {
		if c, out := v.owner(t, "POST", "/api/term/feature/message", map[string]any{"session": rfeLead, "clientId": cid, "thread": rfeLead, "text": text, "anchor": map[string]string{"kind": "feature"}}); c != 200 {
			t.Fatalf("post: %d %v", c, out)
		}
	}

	t.Run("MS4c queued (503), rebound, restarted: no lead gets it, not the old one, not the new one; held", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.feature(t)
		text := "ms4c-owner-comment: pause the migration"
		v.f.set("503")
		post(t, v, "c-ms4c", text)
		if !v.settle(func() bool { return v.f.count("thread-"+rfeLead, text, true) > 0 }) {
			t.Fatalf("precondition: never dispatched")
		}
		other := rebind(t, v)
		v.restart()
		v.f.set("ok")
		for range 10 {
			v.tick(10 * time.Minute)
		}
		if k := v.f.count("thread-"+rfeLead, text, false); k != 0 {
			bad = append(bad, fmt.Sprintf("the old lead kept %d copy(ies) after the rebind and a restart (its queued retry wasn't rechecked)", k))
		}
		if n := v.f.count("thread-"+other, text, true); n != 0 {
			bad = append(bad, fmt.Sprintf("retargeted to the new lead: sent %d time(s)", n))
		}
		if st := v.msgState(t, "c-ms4c"); st["state"] != "held" {
			bad = append(bad, fmt.Sprintf("state after rebind + restart: %v, want held", st))
		}
		scenario(t, "MS4c rebind + restart before delivery: the queued retry is rechecked, no lead gets it, state held", &bad)
	})

	t.Run("MS4d in flight with an unknown outcome, then rebound: the state stays uncertain, never claims held stopped it", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.feature(t)
		text := "ms4d-owner-comment: keep the old schema"
		v.f.mu.Lock()
		v.f.mode, v.f.histDown = "drop", true // Hermes kept it, the answer was lost, the history can't say
		v.f.mu.Unlock()
		post(t, v, "c-ms4d", text)
		if !v.settle(func() bool { return v.f.count("thread-"+rfeLead, text, true) > 0 }) {
			t.Fatalf("precondition: never dispatched")
		}
		other := rebind(t, v)
		v.f.set("ok")
		for range 5 {
			v.tick(10 * time.Minute)
		}
		st := v.msgState(t, "c-ms4d")
		if st["state"] == "held" || st["state"] == "queued" {
			bad = append(bad, fmt.Sprintf("Hermes may already have it (kept, answer lost), but the state is %v: it must say the outcome is unknown, not that it was held back", st))
		}
		if n := v.f.count("thread-"+rfeLead, text, true); n != 1 {
			bad = append(bad, fmt.Sprintf("sent to the old lead %d times, want exactly the one in-flight send (no resend after the rebind)", n))
		}
		if n := v.f.count("thread-"+other, text, true); n != 0 {
			bad = append(bad, "retargeted to the new lead")
		}
		scenario(t, "MS4d unknown in-flight send then a rebind: state honestly uncertain (not held/queued), never resent, never retargeted", &bad)
	})
}
