// Package gapitest is a stand-in for Gapi's voice API for the Studio's tests:
// calls are "placed" into memory and a test says how each one ends.
package gapitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
)

type Placed struct {
	ID        string
	To, From  string
	Agent     gapi.Agent
	Variables map[string]string
	Call      gapi.Call
	Text      *gapi.Transcript
}

type Fake struct {
	*httptest.Server
	mu    sync.Mutex
	Calls []*Placed
	// The next POST /v1/voice/calls is refused with this code (and status), once.
	RefuseCode   string
	RefuseStatus int
	Lines        []gapi.Line
}

func New() *Fake {
	f := &Fake{Lines: []gapi.Line{{ID: "line-1", Number: "+998712000000"}}}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /v1/voice/options", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"voices": []map[string]any{{"id": "charos", "name": "Charos", "available": true}, {"id": "sarvar", "name": "Sarvar", "available": true}},
			"online": true, "g_per_minute": 5, "limits": map[string]any{"prompt_chars": 8000, "vocabulary_terms": 20, "max_duration_seconds": 1800, "variables": 20}})
	})
	mux.HandleFunc("GET /v1/voice/lines", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		write(w, 200, map[string]any{"lines": f.Lines})
	})
	mux.HandleFunc("POST /v1/voice/calls", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Agent     gapi.Agent `json:"agent"`
			To, From  string
			Variables map[string]string `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.RefuseCode != "" {
			code, status := f.RefuseCode, f.RefuseStatus
			f.RefuseCode = ""
			write(w, status, map[string]any{"error": code, "hint": "refused by the test"})
			return
		}
		p := &Placed{ID: fmt.Sprintf("g%d", len(f.Calls)+1), To: req.To, From: req.From, Agent: req.Agent, Variables: req.Variables}
		p.Call = gapi.Call{ID: p.ID, Status: "created"}
		f.Calls = append(f.Calls, p)
		write(w, 201, map[string]any{"call": p.Call})
	})
	mux.HandleFunc("GET /v1/voice/calls/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if p := f.find(r.PathValue("id")); p != nil {
			write(w, 200, p.Call)
			return
		}
		write(w, 404, map[string]any{"error": "voice_call_not_found"})
	})
	mux.HandleFunc("GET /v1/voice/calls/{id}/transcript", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if p := f.find(r.PathValue("id")); p != nil && p.Text != nil {
			write(w, 200, p.Text)
			return
		}
		write(w, 404, map[string]any{"error": "voice_transcript_not_found"})
	})
	mux.HandleFunc("DELETE /v1/voice/calls/{id}", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]any{"ok": true}) })
	f.Server = httptest.NewServer(mux)
	return f
}

func (f *Fake) find(id string) *Placed {
	for _, p := range f.Calls {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Placed lists the calls placed so far.
func (f *Fake) Placed() []*Placed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*Placed(nil), f.Calls...)
}

// Talk ends a call as one that was answered: the agent's and the person's
// lines alternate, the agent first; a line that starts with ">" is the person's.
func (f *Fake) Talk(id, outcome string, at time.Time, lines ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(id)
	seconds := 42.0
	started := at.Add(-42 * time.Second)
	p.Call.Status, p.Call.Outcome, p.Call.EndReason, p.Call.Transcript = "ended", outcome, "agent_hung_up", true
	p.Call.StartedAt, p.Call.EndedAt, p.Call.DurationSeconds, p.Call.ChargedG = &started, &at, &seconds, 3.5
	text := &gapi.Transcript{CallID: id, Outcome: outcome}
	for _, l := range lines {
		role := "agent"
		if strings.HasPrefix(l, ">") {
			role, l = "caller", strings.TrimSpace(l[1:])
		}
		text.Turns = append(text.Turns, gapi.Turn{Role: role, Text: l})
	}
	p.Text = text
}

// Fail ends a call as one that did not reach the person.
func (f *Fake) Fail(id, reason string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.find(id)
	p.Call.Status, p.Call.EndReason, p.Call.EndedAt = "failed", reason, &at
}
