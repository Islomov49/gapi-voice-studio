package api

// The Studio as an MCP server: what a coding assistant (Claude Code, Codex)
// uses to run the Studio for its person. A tool is one of the interface's own
// endpoints, called from inside the process, so the assistant passes through
// exactly the checks the interface does: it cannot start a campaign without an
// accepted test call, dial outside the hours, or take a number off the stop
// list. Two transports: POST /mcp (streamable HTTP, one JSON answer per
// request) and `studio mcp` on standard input and output.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/Islomov49/gapi-voice-studio/server/internal/phone"
)

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// The endpoint the tool is: its method and path for these arguments, and
	// the body to send.
	call func(a map[string]any) (method, path string, body any)
	// A tool that is not one plain JSON request.
	custom func(s *Server, ctx context.Context, ws *Workspace, a map[string]any) (int, []byte)
}

func obj(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func any_(desc string) map[string]any { return map[string]any{"type": "object", "description": desc} }

func arg(a map[string]any, key string) string {
	v, _ := a[key].(string)
	return url.PathEscape(strings.TrimSpace(v))
}

func pick(a map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := a[k]; ok {
			out[k] = v
		}
	}
	return out
}

const specDoc = `The agent's description: {"company": the business's name as said aloud, "script": the instruction in the callers' language with {{variable}} places ` +
	`(put {{studio_greeting}} where the agent says its first words), "voice": "charos" or "sarvar", "variables": {name: default}, "vocabulary": [names the recogniser must hear right], ` +
	`"max_call_seconds": 30-1800, "form": [{"key", "title", "type": text|number|choice|rating|yesno|datetime, "options": [for choice], "ask": how the agent asks, "required"}]}`

var mcpTools = []mcpTool{
	{Name: "studio_status", Description: "The project as it stands: phone lines and voices in Gapi, the tester's number, calls in progress, and what is still missing before a call (\"needs\"). Start here.",
		InputSchema: obj(nil, map[string]any{}),
		call:        func(a map[string]any) (string, string, any) { return "GET", "/api/me", nil }},
	{Name: "set_tester_phone", Description: "Save the number the agent calls first to show how it talks. Ask the person for it; never guess a number.",
		InputSchema: obj([]string{"phone"}, map[string]any{"phone": str("The tester's number, like +998901234567")}),
		call: func(a map[string]any) (string, string, any) {
			return "PUT", "/api/workspace", map[string]any{"tester_phone": a["phone"]}
		}},
	{Name: "list_templates", Description: "The task types a new agent can start from (feedback, lead qualification, reminder, win-back), each with its script and form.",
		InputSchema: obj(nil, map[string]any{}),
		call:        func(a map[string]any) (string, string, any) { return "GET", "/api/templates", nil }},
	{Name: "list_agents", Description: "The project's agents with their newest version and whether a test call of it was accepted.",
		InputSchema: obj(nil, map[string]any{}),
		call:        func(a map[string]any) (string, string, any) { return "GET", "/api/agents", nil }},
	{Name: "get_agent", Description: "One agent: its newest version's description, what keeps it from being called with (\"problems\"), its variables, the whole instruction as the model reads it, and its versions.",
		InputSchema: obj([]string{"agent_id"}, map[string]any{"agent_id": str("The agent's id")}),
		call:        func(a map[string]any) (string, string, any) { return "GET", "/api/agents/" + arg(a, "agent_id"), nil }},
	{Name: "create_agent", Description: "Make an agent, from a template (then rewrite its first paragraph and its facts with save_agent_version) or from a description. " + specDoc,
		InputSchema: obj([]string{"name"}, map[string]any{"name": str("The agent's name"), "template": str("A template's id from list_templates"), "spec": any_("The description, instead of a template")}),
		call: func(a map[string]any) (string, string, any) {
			body := pick(a, "name", "template", "spec")
			body["author"] = "assistant"
			return "POST", "/api/agents", body
		}},
	{Name: "save_agent_version", Description: "Change an agent: send its whole description, changed. This makes a new version, which needs a test call accepted by the tester before any campaign may use it. " +
		"Answers the problems the new version has; fix them before calling. " + specDoc,
		InputSchema: obj([]string{"agent_id", "spec"}, map[string]any{"agent_id": str("The agent's id"), "spec": any_("The whole description"), "note": str("What changed and why, in a sentence")}),
		call: func(a map[string]any) (string, string, any) {
			body := pick(a, "spec", "note")
			body["author"] = "assistant"
			return "POST", "/api/agents/" + arg(a, "agent_id") + "/versions", body
		}},
	{Name: "start_test_call", Description: "Have the agent call the tester with a version of its script (the newest if none is named). A real phone rings: do this when the person expects it. " +
		"Answers the call's id; follow it with get_call.",
		InputSchema: obj([]string{"version_id"}, map[string]any{"version_id": str("The version's id (\"latest\".\"id\" of get_agent)"),
			"values": any_("Values for the script's variables on this call, {name: value}"), "phone": str("Another number to call instead of the saved tester's; only one the person gave")}),
		call: func(a map[string]any) (string, string, any) {
			return "POST", "/api/calls/test", pick(a, "version_id", "values", "phone")
		}},
	{Name: "get_call", Description: "One call: its state, how it ended, its text turn by turn, the checks (did the agent introduce itself as an AI assistant, did the person ask not to be called), its form and the answers so far. " +
		"\"settled\": false means it is still ringing or going on; ask again in a few seconds.",
		InputSchema: obj([]string{"call_id"}, map[string]any{"call_id": str("The call's id")}),
		call:        func(a map[string]any) (string, string, any) { return "GET", "/api/calls/" + arg(a, "call_id"), nil }},
	{Name: "list_calls", Description: "Recent calls, newest first. needs_form leaves the answered calls whose form is still empty: the ones to read and fill with fill_call_form.",
		InputSchema: obj(nil, map[string]any{"campaign_id": str("Only this campaign's calls"), "kind": str("\"test\" or \"campaign\""), "needs_form": flag("Only answered calls with an empty form")}),
		call: func(a map[string]any) (string, string, any) {
			q := url.Values{"limit": {"50"}}
			for _, k := range []string{"campaign_id", "kind"} {
				if v, _ := a[k].(string); v != "" {
					q.Set(k, v)
				}
			}
			if a["needs_form"] == true {
				q.Set("needs_form", "1")
			}
			return "GET", "/api/calls?" + q.Encode(), nil
		}},
	{Name: "record_tester_verdict", Description: "Record what the tester said about a test call they took. accepted: true only when the tester themself said the call is good; it is what lets campaigns use that version. " +
		"Never decide it yourself and never accept a call the tester has not heard. A call in which the agent did not introduce itself as an AI assistant is refused.",
		InputSchema: obj([]string{"call_id", "accepted", "tester_words"}, map[string]any{"call_id": str("The test call's id"), "accepted": flag("The tester accepted the call"),
			"tester_words": str("What the tester said about the call, in their words")}),
		call: func(a map[string]any) (string, string, any) {
			return "POST", "/api/calls/" + arg(a, "call_id") + "/verdict", map[string]any{"accepted": a["accepted"], "feedback": a["tester_words"]}
		}},
	{Name: "fill_call_form", Description: "Write a call's form from its text: for each field you found an answer to, the value and the caller's words it was taken from, copied exactly from the call's text. " +
		"An answer whose quote is not in the text is refused; leave out a field nobody answered. Values: rating 1-5 (number), yesno true/false, choice one of the field's options, datetime \"2026-11-14T15:30\".",
		InputSchema: obj([]string{"call_id", "answers"}, map[string]any{"call_id": str("The call's id"),
			"answers": any_("{field key: {\"value\": ..., \"quote\": \"the words of the call\"}}"), "summary": str("What the call came to, in one or two sentences, in the person's language")}),
		call: func(a map[string]any) (string, string, any) {
			return "PUT", "/api/calls/" + arg(a, "call_id") + "/form", pick(a, "answers", "summary")
		}},
	{Name: "import_contacts", Description: "Upload a list of people to call: an .xlsx or .csv whose first row is the headers. One column holds the phone numbers; the others become the script's variables. " +
		"Answers how many rows were accepted, duplicated, without a valid number or on the stop list, and the variables' names.",
		InputSchema: obj([]string{"filename"}, map[string]any{"filename": str("The file's name with its extension, e.g. clients.xlsx"), "text": str("A .csv file's text"),
			"content_base64": str("An .xlsx file's bytes, base64"), "name": str("The list's name"), "phone_column": str("The header of the column with the numbers, when it is not guessed")}),
		custom: importContacts},
	{Name: "list_contact_lists", Description: "The uploaded lists with their sizes and columns.",
		InputSchema: obj(nil, map[string]any{}),
		call:        func(a map[string]any) (string, string, any) { return "GET", "/api/lists", nil }},
	{Name: "create_campaign", Description: "Make a campaign as a draft: an agent's version, a list and the calling rules. Nothing is dialled until start_campaign. " +
		"rules (all optional, the defaults are sound): {\"from\": \"09:00\", \"to\": \"20:00\" (Tashkent time, never outside 08:00-21:00), \"days\": [1..7, 1 = Monday], \"max_attempts\": 1-5, " +
		"\"retry_minutes\", \"first_portion\": people in the first portion, \"portion\": people in the next ones}.",
		InputSchema: obj([]string{"name", "version_id", "list_id"}, map[string]any{"name": str("The campaign's name"), "version_id": str("The agent version to call with"),
			"list_id": str("The list to call"), "line": str("The Gapi line to call from, when the project has several"), "rules": any_("The calling rules")}),
		call: func(a map[string]any) (string, string, any) {
			return "POST", "/api/campaigns", pick(a, "name", "version_id", "list_id", "line", "rules")
		}},
	{Name: "list_campaigns", Description: "The project's campaigns with their state and how many people each has been through.",
		InputSchema: obj(nil, map[string]any{}),
		call:        func(a map[string]any) (string, string, any) { return "GET", "/api/campaigns", nil }},
	{Name: "get_campaign", Description: "One campaign: its state, why it is paused if it is, the portion it is on, people by where they stand, results by kind, calls placed and answered, minutes spent.",
		InputSchema: obj([]string{"campaign_id"}, map[string]any{"campaign_id": str("The campaign's id")}),
		call: func(a map[string]any) (string, string, any) {
			return "GET", "/api/campaigns/" + arg(a, "campaign_id"), nil
		}},
	{Name: "start_campaign", Description: "Start a draft campaign, or let a paused one go on. Real phones ring: do it only when the person asked for it. Refused until the tester has accepted a test call of the campaign's agent version. " +
		"Resuming a campaign that paused itself tells it the person has looked at the reason and lets that portion pass.",
		InputSchema: obj([]string{"campaign_id"}, map[string]any{"campaign_id": str("The campaign's id")}),
		call: func(a map[string]any) (string, string, any) {
			return "POST", "/api/campaigns/" + arg(a, "campaign_id") + "/start", nil
		}},
	{Name: "pause_campaign", Description: "Stop a running campaign from placing new calls; calls in progress finish.",
		InputSchema: obj([]string{"campaign_id"}, map[string]any{"campaign_id": str("The campaign's id")}),
		call: func(a map[string]any) (string, string, any) {
			return "POST", "/api/campaigns/" + arg(a, "campaign_id") + "/pause", nil
		}},
	{Name: "campaign_results", Description: "A campaign's people, 200 at a time: where each stands, attempts, the result, and the form of their last call. Numbers are masked.",
		InputSchema: obj([]string{"campaign_id"}, map[string]any{"campaign_id": str("The campaign's id"), "status": str("pending, calling, done, failed or skipped"),
			"offset": map[string]any{"type": "integer", "description": "Rows to skip"}}),
		call: func(a map[string]any) (string, string, any) {
			q := url.Values{}
			if v, _ := a["status"].(string); v != "" {
				q.Set("status", v)
			}
			if n, ok := a["offset"].(float64); ok && n > 0 {
				q.Set("offset", fmt.Sprint(int(n)))
			}
			return "GET", "/api/campaigns/" + arg(a, "campaign_id") + "/targets?" + q.Encode(), nil
		}},
	{Name: "add_to_stop_list", Description: "Put numbers on the project's stop list: they are never called. Taking a number off the list is done by the person in the interface, not here.",
		InputSchema: obj([]string{"phones"}, map[string]any{"phones": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The numbers"},
			"reason": str("Why")}),
		call: func(a map[string]any) (string, string, any) {
			return "POST", "/api/stoplist", pick(a, "phones", "reason")
		}},
}

func importContacts(s *Server, ctx context.Context, ws *Workspace, a map[string]any) (int, []byte) {
	name, _ := a["filename"].(string)
	var data []byte
	if text, _ := a["text"].(string); text != "" {
		data = []byte(text)
	} else if b64, _ := a["content_base64"].(string); b64 != "" {
		var err error
		if data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(b64)); err != nil {
			return 400, []byte(`{"error":"file_invalid","hint":"content_base64 is not base64"}`)
		}
	} else {
		return 400, []byte(`{"error":"file_missing","hint":"send the file as text (.csv) or content_base64 (.xlsx)"}`)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", name)
	_, _ = part.Write(data)
	for _, k := range []string{"name", "phone_column"} {
		if v, _ := a[k].(string); v != "" {
			_ = mw.WriteField(k, v)
		}
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/lists", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return s.inside(ctx, ws, req)
}

// inside runs one of the interface's endpoints for a workspace.
func (s *Server) inside(ctx context.Context, ws *Workspace, req *http.Request) (int, []byte) {
	rec := httptest.NewRecorder()
	s.inner.ServeHTTP(rec, req.WithContext(context.WithValue(ctx, workspaceKey{}, ws)))
	return rec.Code, rec.Body.Bytes()
}

// maskPhones hides the middle of every number in an answer that goes to an
// assistant: it works with people by their rows and names, not their numbers.
func maskPhones(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if text, ok := val.(string); ok && k == "phone" {
				x[k] = phone.Mask(text)
			} else {
				x[k] = maskPhones(val)
			}
		}
	case []any:
		for i := range x {
			x[i] = maskPhones(x[i])
		}
	}
	return v
}

const mcpInstructions = `Gapi Voice Agent Studio: voice agents that call people by phone, for the project this server is connected to. ` +
	`The order is fixed: studio_status; an agent (create_agent, save_agent_version until it has no problems); a test call to the tester (start_test_call, get_call) and the tester's own verdict (record_tester_verdict); ` +
	`on feedback, change the script and call the tester again; only then a list (import_contacts), a campaign (create_campaign, start_campaign) and its results (get_campaign, list_calls with needs_form, fill_call_form). ` +
	`Every start_test_call and start_campaign rings real phones and spends the project's Gapi balance: do them when the person asked. The repository's AGENTS.md has the working rules.`

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var mcpVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// MCP answers one JSON-RPC message; nil for a notification.
func (s *Server) MCP(ctx context.Context, ws *Workspace, raw []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Method == "" {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "not a JSON-RPC request"}}
	}
	if len(req.ID) == 0 {
		return nil // a notification: nothing to answer
	}
	answer := func(result any) *rpcResponse { return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result} }
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := mcpVersions[0]
		for _, v := range mcpVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return answer(map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "gapi-voice-studio", "version": s.Version}, "instructions": mcpInstructions})
	case "ping":
		return answer(map[string]any{})
	case "tools/list":
		return answer(map[string]any{"tools": mcpTools})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32602, "params are {name, arguments}"}}
		}
		for _, t := range mcpTools {
			if t.Name != p.Name {
				continue
			}
			if p.Arguments == nil {
				p.Arguments = map[string]any{}
			}
			var status int
			var body []byte
			if t.custom != nil {
				status, body = t.custom(s, ctx, ws, p.Arguments)
			} else {
				method, path, payload := t.call(p.Arguments)
				var rd io.Reader
				if payload != nil {
					b, _ := json.Marshal(payload)
					rd = bytes.NewReader(b)
				}
				status, body = s.inside(ctx, ws, httptest.NewRequest(method, path, rd))
			}
			var parsed any
			if json.Unmarshal(body, &parsed) == nil {
				body, _ = json.Marshal(maskPhones(parsed))
			}
			return answer(map[string]any{"content": []map[string]any{{"type": "text", "text": string(body)}}, "isError": status >= 400})
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32602, "no tool " + p.Name}}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32601, "no method " + req.Method}}
}

// mcpHTTP: POST /mcp. The project is the one whose Gapi token comes as
// "Authorization: Bearer", or the service's own (STUDIO_GAPI_TOKEN).
func (s *Server) mcpHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP server answers POST only", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = s.DefaultToken
	}
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="gapi-voice-studio"`)
		http.Error(w, "send the Gapi project token as Authorization: Bearer, or start the Studio with STUDIO_GAPI_TOKEN", http.StatusUnauthorized)
		return
	}
	ws, err := s.workspaceForToken(r.Context(), token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, "the request could not be read", http.StatusBadRequest)
		return
	}
	resp := s.MCP(r.Context(), ws, raw)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, 200, resp)
}

// ServeMCP speaks MCP over standard input and output, a message a line, for
// the project of the service's own token.
func (s *Server) ServeMCP(ctx context.Context, in io.Reader, out io.Writer) error {
	if s.inner == nil {
		s.Handler()
	}
	ws, err := s.workspaceForToken(ctx, s.DefaultToken)
	if err != nil {
		return fmt.Errorf("STUDIO_GAPI_TOKEN: %w", err)
	}
	lines := bufio.NewScanner(in)
	lines.Buffer(make([]byte, 1<<20), 32<<20)
	enc := json.NewEncoder(out)
	for lines.Scan() {
		if len(bytes.TrimSpace(lines.Bytes())) == 0 {
			continue
		}
		// the workspace's name and tester may have changed since the last message
		if fresh, err := s.workspaceForToken(ctx, s.DefaultToken); err == nil {
			ws = fresh
		}
		if resp := s.MCP(ctx, ws, lines.Bytes()); resp != nil {
			if err := enc.Encode(resp); err != nil {
				return err
			}
		}
	}
	return lines.Err()
}
