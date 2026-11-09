package main

// Tests for delivering team news (notes) to a thread's lead, end to end through a fake Hermes this suite controls: a
// note reaches the lead's stored history exactly once, across 503s, a lost answer, an unqueue, a restart and more notes
// than the notes store's cap. "Delivered" means Hermes kept the message in the lead's history, never that the feature's
// work was done, so each case also checks that delivery doesn't mark the thread done. Identifiers are rfe-prefixed
// because the panel has its own delivery_test.go.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// rfeLead is the id of the lead thread (and its pane session) that the tests deliver to.
const rfeLead = "rfe-lead"

// rfeHermes is a fake Hermes API server. It keeps the user messages it accepted per session (hist), records every turn
// it was asked for (tries), and its mode makes chat requests succeed or fail in the ways the tests need.
type rfeHermes struct {
	mu       sync.Mutex
	mode     string              // ok | 503 | drop (stored, then the connection closes unanswered) | hang (stored, answered on release)
	hist     map[string][]string // session -> user messages kept
	tries    map[string][]string // session -> every turn asked for, kept or not (503, dropped, hung)
	histDown bool                // the history (GET …/messages) answers 503
	release  chan struct{}
}

// newRFEHermes starts a fake Hermes in ok mode (stopped when the test ends) and returns it with its base URL.
func newRFEHermes(t *testing.T) (*rfeHermes, string) {
	f := &rfeHermes{mode: "ok", hist: map[string][]string{}, tries: map[string][]string{}, release: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/detailed", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"platforms":{}}`) })
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) })
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	mux.HandleFunc("GET /api/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.histDown {
			http.Error(w, "history unavailable", 503)
			return
		}
		l := []map[string]any{}
		if r.URL.Query().Get("offset") == "" || r.URL.Query().Get("offset") == "0" {
			for i, m := range f.hist[r.PathValue("id")] {
				l = append(l, map[string]any{"id": i + 1, "role": "user", "content": m, "timestamp": i + 1})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": l})
	})
	mux.HandleFunc("POST /api/sessions/{id}/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Message string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		mode := f.mode
		f.tries[r.PathValue("id")] = append(f.tries[r.PathValue("id")], b.Message)
		if mode != "503" {
			f.hist[r.PathValue("id")] = append(f.hist[r.PathValue("id")], b.Message)
		}
		rel := f.release
		f.mu.Unlock()
		switch mode {
		case "503":
			http.Error(w, "offline", 503)
			return
		case "drop":
			if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
				c.(net.Conn).Close()
			}
			return
		case "hang":
			select {
			case <-rel:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, "event: assistant.completed\ndata: {\"content\":\"ok\"}\n\nevent: run.completed\ndata: {\"messages\":[1]}\n\n")
	})
	h := httptest.NewServer(mux)
	t.Cleanup(h.Close)
	return f, h.URL
}

// set switches the fake into mode for the chat requests that follow.
func (f *rfeHermes) set(mode string) { f.mu.Lock(); f.mode = mode; f.mu.Unlock() }

// kept counts the copies of the lead's kept messages that carry the note's line ("#<id> ").
func (f *rfeHermes) kept(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.hist["thread-"+rfeLead] {
		if strings.Contains(m, fmt.Sprintf("#%d ", id)) {
			n++
		}
	}
	return n
}

// tried counts the turns the panel asked the lead's Hermes for that carried the note's line, whatever the answer.
func (f *rfeHermes) tried(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.tries["thread-"+rfeLead] {
		if strings.Contains(m, fmt.Sprintf("#%d ", id)) {
			n++
		}
	}
	return n
}

// dispatched reports whether the note went into the real dispatch path (a request reached Hermes) while the delivery
// cursor stayed short of it; if not, it says why. It keeps ticking the clock for up to 5 s while it waits.
func (v *rfeEnv) dispatched(id int64) (bool, string) {
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if v.f.tried(id) > 0 {
			if s := v.seen(); s >= id {
				return false, fmt.Sprintf("the cursor (%d) passed note %d before Hermes kept it", s, id)
			}
			return true, ""
		}
		v.tick(10 * time.Minute)
	}
	return false, fmt.Sprintf("note %d never reached Hermes: nothing was dispatched", id)
}

// rfeEnv is a panel test server whose assistant talks to a fake Hermes, with a fake clock for the delivery loop.
type rfeEnv struct {
	e   *testEnv
	f   *rfeHermes
	a   *assistant
	now time.Time
}

// newRFEEnv builds an rfeEnv with one working lead thread (rfeLead) and runs a first delivery pass.
func newRFEEnv(t *testing.T) *rfeEnv {
	e := newTestServer(t)
	f, base := newRFEHermes(t)
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	os.WriteFile(key, []byte("k\n"), 0o600)
	a := newAssistant(base, key, "")
	a.threadsFile, a.leadFile = filepath.Join(dir, "threads.json"), filepath.Join(dir, "LEAD.md")
	os.WriteFile(a.leadFile, []byte("Lead of {title}."), 0o600)
	e.s.ai = a
	a.mu.Lock()
	th := a.newThread("RFE lead", "")
	th.Status, th.Created = "working", 1
	a.saveThreads()
	a.mu.Unlock()
	v := &rfeEnv{e: e, f: f, a: a, now: time.Now()}
	v.tick(0)
	return v
}

// news pushes a "done" note from the lead's pane and returns the note's id.
func (v *rfeEnv) news(title string) int64 {
	v.e.s.notes.push("done", "ok", title, "", "pane:"+rfeLead+":3")
	return v.e.s.notes.lastID()
}

// tick advances the fake clock by after and runs one team-delivery pass.
func (v *rfeEnv) tick(after time.Duration) { v.now = v.now.Add(after); v.e.s.teamDeliver(v.now) }

// seen returns the lead thread's delivery cursor: the id of the newest note acknowledged as delivered.
func (v *rfeEnv) seen() int64 {
	v.a.mu.Lock()
	defer v.a.mu.Unlock()
	return v.a.thread(rfeLead).TeamSeen
}

// status returns the lead thread's status.
func (v *rfeEnv) status() string {
	v.a.mu.Lock()
	defer v.a.mu.Unlock()
	return v.a.thread(rfeLead).Status
}

// restart simulates a panel restart: the panel comes back from disk (threads, their outbox, the notes) and the old
// assistant can't write any more.
func (v *rfeEnv) restart() {
	old := v.a
	old.mu.Lock()
	file, lead := old.threadsFile, old.leadFile
	old.threadsFile = ""
	old.mu.Unlock()
	b := newAssistant(old.base, old.keyFile, "")
	b.threadsFile, b.leadFile = file, lead
	b.loadThreads()
	v.a, v.e.s.ai = b, b
	v.e.s.notes = newNotifier(v.e.s.notes.file)
}

// settle ticks (well past any backoff) until cond holds or 10 s pass, and reports whether cond held.
func (v *rfeEnv) settle(cond func() bool) bool {
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		v.tick(10 * time.Minute)
		if cond() {
			return true
		}
	}
	return false
}

// TestVMAgentDelivery checks that a note reaches the lead exactly once when the answer is lost, when the hand-off is
// unqueued, across a 503 and a restart, and when more notes arrive than the store keeps.
func TestVMAgentDelivery(t *testing.T) {
	t.Run("D1 a lost answer: Hermes kept it, the restart finds it in history, no second copy", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.f.set("drop")
		id := v.news("blocker: the deploy key is missing")
		v.tick(time.Second)
		if ok, why := v.dispatched(id); !ok {
			bad = append(bad, why)
		}
		v.f.set("ok")
		v.restart()
		if !v.settle(func() bool { return v.seen() >= id }) {
			bad = append(bad, fmt.Sprintf("after the restart the cursor never acknowledged it (cursor %d, note %d)", v.seen(), id))
		}
		if n := v.f.kept(id); n != 1 {
			bad = append(bad, fmt.Sprintf("the lead's history has %d copies of the note", n))
		}
		if v.status() == "done" {
			bad = append(bad, "delivery marked the thread done")
		}
		scenario(t, "D1 lost reply: history lookup acknowledges it, exactly one copy, nothing marked done", &bad)
	})

	t.Run("D2 an unqueued hand-off isn't delivered: it goes again later, once", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.f.set("hang")
		v.a.send(rfeLead, "[From the owner, in the panel]\nkeep going") // the lead is mid-turn
		time.Sleep(100 * time.Millisecond)
		id := v.news("tests failed on the feature branch")
		v.tick(time.Second)
		qid := ""
		v.a.mu.Lock()
		for _, q := range v.a.conv(rfeLead).queue {
			if strings.Contains(q.Text, fmt.Sprintf("#%d ", id)) {
				qid = q.ID
			}
		}
		v.a.mu.Unlock()
		if qid == "" || !v.a.unqueue(rfeLead, qid) {
			bad = append(bad, "the hand-off wasn't queued behind the running turn, or couldn't be unqueued")
		}
		v.tick(time.Second)
		if v.seen() >= id {
			bad = append(bad, "unqueueing it acknowledged it")
		}
		v.f.set("ok")
		close(v.f.release)
		if !v.settle(func() bool { return v.seen() >= id && v.f.kept(id) == 1 }) {
			bad = append(bad, fmt.Sprintf("after the unqueue: cursor %d (note %d), copies kept %d", v.seen(), id, v.f.kept(id)))
		}
		if v.f.tried(id) == 0 {
			bad = append(bad, "Hermes never received it")
		}
		if v.status() == "done" {
			bad = append(bad, "delivery marked the thread done")
		}
		scenario(t, "D2 unqueue is not delivery: resent later, exactly one copy kept", &bad)
	})

	t.Run("D3 a 503 then a restart: pending survives and is delivered once", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.f.set("503")
		id := v.news("approval waiting on Builder")
		v.tick(time.Second)
		if ok, why := v.dispatched(id); !ok {
			bad = append(bad, why)
		}
		v.restart()
		v.f.set("ok")
		if !v.settle(func() bool { return v.seen() >= id && v.f.kept(id) == 1 }) {
			bad = append(bad, fmt.Sprintf("after the restart: cursor %d (note %d), copies kept %d", v.seen(), id, v.f.kept(id)))
		}
		scenario(t, "D3 503 + restart: kept pending, delivered once after", &bad)
	})

	t.Run("D4 more notes than the store's cap while the lead is unreachable: none undelivered is lost", func(t *testing.T) {
		var bad []string
		v := newRFEEnv(t)
		v.f.set("503")
		first := v.news("first blocker, before the flood")
		v.tick(time.Second)
		if ok, why := v.dispatched(first); !ok {
			bad = append(bad, why)
		}
		for i := range maxNotes + 50 {
			v.e.s.notes.push("ci", "bad", fmt.Sprintf("ci failure %d", i), "", "ci") // others' news fills the bell
		}
		last := v.news("last blocker, after the flood")
		v.tick(time.Second)
		have := map[int64]bool{}
		for _, n := range v.e.s.notes.since(0) {
			have[n.ID] = true
		}
		if !have[first] {
			bad = append(bad, fmt.Sprintf("the undelivered note #%d was evicted by the cap", first))
		}
		v.f.set("ok")
		if !v.settle(func() bool { return v.f.kept(first) == 1 && v.f.kept(last) == 1 && v.seen() >= last }) {
			bad = append(bad, fmt.Sprintf("after the lead came back: first kept %d, last kept %d, cursor %d (last %d)", v.f.kept(first), v.f.kept(last), v.seen(), last))
		}
		scenario(t, "D4 pending beyond the note cap: kept until delivered, each once", &bad)
	})
}
