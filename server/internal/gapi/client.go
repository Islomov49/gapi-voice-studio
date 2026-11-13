// Package gapi is the Studio's side of Gapi's public voice API
// (https://console.gapi.uz/docs: agents, calls to a phone, a call's text and
// recording). The Studio is an ordinary client of that API: everything goes
// through a project's token.
package gapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	Base string // https://console.gapi.uz
	HTTP *http.Client
}

func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Error is an answer the API refused with: its code ("voice_no_capacity",
// "quota_exceeded", ...) and the hint beside it.
type Error struct {
	Status int
	Code   string
	Hint   string
}

func (e *Error) Error() string { return fmt.Sprintf("gapi %d %s: %s", e.Status, e.Code, e.Hint) }

// Code of an API refusal, or "" for anything else (a network error).
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func (c *Client) do(ctx context.Context, token, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode}
		var parsed struct{ Error, Hint string }
		if json.Unmarshal(data, &parsed) == nil && parsed.Error != "" {
			e.Code, e.Hint = parsed.Error, parsed.Hint
		} else {
			e.Code, e.Hint = "http_error", strings.TrimSpace(string(data[:min(len(data), 200)]))
		}
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

type Voice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Gender    string `json:"gender"`
	Available bool   `json:"available"`
}

type Options struct {
	Voices     []Voice `json:"voices"`
	Online     bool    `json:"online"`
	FreeCalls  int     `json:"free_calls"`
	GPerMinute float64 `json:"g_per_minute"`
	Limits     struct {
		PromptChars        int `json:"prompt_chars"`
		VocabularyTerms    int `json:"vocabulary_terms"`
		MaxDurationSeconds int `json:"max_duration_seconds"`
		Variables          int `json:"variables"`
	} `json:"limits"`
}

func (c *Client) Options(ctx context.Context, token string) (*Options, error) {
	var o Options
	return &o, c.do(ctx, token, http.MethodGet, "/v1/voice/options", nil, &o)
}

type Line struct {
	ID           string `json:"id"`
	Number       string `json:"number"`
	Registration string `json:"registration,omitempty"`
}

func (c *Client) Lines(ctx context.Context, token string) ([]Line, error) {
	var out struct {
		Lines []Line `json:"lines"`
	}
	return out.Lines, c.do(ctx, token, http.MethodGet, "/v1/voice/lines", nil, &out)
}

// Agent is an agent described whole in the call that uses it: the Studio
// keeps agents and their versions itself and hands Gapi the one it calls with.
type Agent struct {
	Name               string            `json:"name"`
	Prompt             string            `json:"prompt"`
	Voice              string            `json:"voice,omitempty"`
	FirstSpeaker       string            `json:"first_speaker,omitempty"`
	FirstLine          string            `json:"first_line,omitempty"`
	Vocabulary         []string          `json:"vocabulary,omitempty"`
	MaxDurationSeconds int               `json:"max_duration_seconds,omitempty"`
	Variables          map[string]string `json:"variables,omitempty"`
}

type Call struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"` // created, active, ended, failed
	Phone           string     `json:"phone,omitempty"`
	StartedAt       *time.Time `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	DurationSeconds *float64   `json:"duration_seconds"`
	EndReason       string     `json:"end_reason,omitempty"`
	ChargedG        float64    `json:"charged_g"`
	Recording       bool       `json:"recording"`
	Outcome         string     `json:"outcome,omitempty"`
	Transcript      bool       `json:"transcript"`
}

// CallPhone has the agent call a phone from one of the project's lines. The
// answer comes at once, the call "created"; Get follows it from there.
func (c *Client) CallPhone(ctx context.Context, token string, agent Agent, to, from string, variables map[string]string) (*Call, error) {
	body := map[string]any{"agent": agent, "to": to}
	if from != "" {
		body["from"] = from
	}
	if len(variables) > 0 {
		body["variables"] = variables
	}
	var out struct {
		Call Call `json:"call"`
	}
	return &out.Call, c.do(ctx, token, http.MethodPost, "/v1/voice/calls", body, &out)
}

func (c *Client) Get(ctx context.Context, token, id string) (*Call, error) {
	var call Call
	return &call, c.do(ctx, token, http.MethodGet, "/v1/voice/calls/"+id, nil, &call)
}

func (c *Client) End(ctx context.Context, token, id string) error {
	return c.do(ctx, token, http.MethodDelete, "/v1/voice/calls/"+id, nil, nil)
}

type Turn struct {
	Role        string `json:"role"` // caller, agent, event, function
	Text        string `json:"text,omitempty"`
	Name        string `json:"name,omitempty"`
	AtMs        int64  `json:"at_ms,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
}

type Transcript struct {
	CallID  string `json:"call_id"`
	Outcome string `json:"outcome"`
	Turns   []Turn `json:"turns"`
}

func (c *Client) Transcript(ctx context.Context, token, id string) (*Transcript, error) {
	var t Transcript
	return &t, c.do(ctx, token, http.MethodGet, "/v1/voice/calls/"+id+"/transcript", nil, &t)
}

// Recording streams the call's recording (WAV) to w, with the size and type
// Gapi answered with so that a browser's player can show its length.
func (c *Client) Recording(ctx context.Context, token, id string, w http.ResponseWriter, rangeHeader string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/v1/voice/calls/"+id+"/recording", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	// no client timeout: a recording of a long call is tens of megabytes
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Code: "recording_unavailable"}
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}
