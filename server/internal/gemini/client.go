// Package gemini is the little of Google's Gemini API the Studio uses, when a
// person gives it their own key: one question answered as JSON of a given
// shape (the Interactions API, https://aistudio.google.com/api-reference),
// and the list of models, which is also how a key is checked.
package gemini

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

// DefaultModel is Google's alias for its current Flash model: reading one
// call's text is a small job, and the alias outlives model names.
const DefaultModel = "gemini-flash-latest"

type Client struct {
	Base string
	HTTP *http.Client
}

func New(base string) *Client {
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Error is Gemini's refusal: Code is its word (authentication,
// model_not_found, rate_limit_exceeded, ...), or the HTTP status as text.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("gemini: %d %s: %s", e.Status, e.Code, e.Message) }

// Code is the refusal's word, or "" for an error that is not Gemini's answer.
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func (c *Client) do(ctx context.Context, method, path, key string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("x-goog-api-key", key)
	req.Header.Set("Api-Revision", "2026-05-20")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode, Code: http.StatusText(resp.StatusCode)}
		// the code is a word in the Interactions API and a number in the older one
		var refusal struct {
			Error struct {
				Code    any    `json:"code"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &refusal) == nil {
			if word, ok := refusal.Error.Code.(string); ok && word != "" {
				e.Code = word
			} else if refusal.Error.Status != "" {
				e.Code = refusal.Error.Status
			}
			e.Message = refusal.Error.Message
		}
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 403 || (resp.StatusCode == 400 && strings.Contains(strings.ToLower(e.Message), "api key")):
			e.Code = "authentication"
		case resp.StatusCode == 404:
			e.Code = "model_not_found"
		case resp.StatusCode == 429:
			e.Code = "rate_limit_exceeded"
		}
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Models lists the ids of the models the key may use ("gemini-3.8-flash").
func (c *Client) Models(ctx context.Context, key string) ([]string, error) {
	var out []string
	token := ""
	for page := 0; page < 5; page++ {
		var resp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		path := "/v1beta/models?pageSize=200"
		if token != "" {
			path += "&pageToken=" + token
		}
		if err := c.do(ctx, "GET", path, key, nil, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Models {
			out = append(out, strings.TrimPrefix(m.Name, "models/"))
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

// JSON asks one question and reads the answer, which Gemini is held to the
// schema for, into out. The request is not kept on Google's side (store: false).
func (c *Client) JSON(ctx context.Context, key, model, system, input string, schema, out any) error {
	if model == "" {
		model = DefaultModel
	}
	body := map[string]any{
		"model":              model,
		"system_instruction": system,
		"input":              input,
		"store":              false,
		"generation_config":  map[string]any{"thinking_level": "low", "max_output_tokens": 8000},
		"response_format":    map[string]any{"type": "text", "mime_type": "application/json", "schema": schema},
	}
	var resp struct {
		Status string `json:"status"`
		Steps  []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"steps"`
	}
	if err := c.do(ctx, "POST", "/v1beta/interactions", key, body, &resp); err != nil {
		return err
	}
	var text strings.Builder
	for _, s := range resp.Steps {
		if s.Type != "model_output" {
			continue
		}
		for _, part := range s.Content {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
	}
	if text.Len() == 0 {
		return &Error{Status: 200, Code: "empty_answer", Message: "the model answered nothing (status " + resp.Status + ")"}
	}
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		return &Error{Status: 200, Code: "answer_not_json", Message: err.Error()}
	}
	return nil
}
