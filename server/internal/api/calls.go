package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Islomov49/gapi-voice-studio/server/internal/compile"
	"github.com/Islomov49/gapi-voice-studio/server/internal/dialer"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
	"github.com/Islomov49/gapi-voice-studio/server/internal/phone"
)

// testCall: POST /api/calls/test {"version_id", "phone", "values", "line"}.
// The agent calls the tester: the workspace's tester number unless another is
// given. This is the call a campaign cannot start without.
func (s *Server) testCall(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		VersionID string            `json:"version_id"`
		Phone     string            `json:"phone"`
		Values    map[string]string `json:"values"`
		Line      string            `json:"line"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	name, v, err := s.version(r.Context(), ws, req.VersionID)
	if err != nil {
		return err
	}
	number := ws.TesterPhone
	if strings.TrimSpace(req.Phone) != "" {
		var ok bool
		if number, ok = phone.Normalize(req.Phone); !ok {
			return bad("phone_invalid", "write the number as +998901234567")
		}
	}
	if number == "" {
		return conflict("tester_phone_needed", "whose phone should the agent call for the test? save the tester's number first")
	}
	if p := compile.Check(v.Spec, s.limits(r.Context(), ws)); len(p) > 0 {
		return &fail{Status: http.StatusBadRequest, Code: "agent_invalid", Hint: p[0].Text, Extra: map[string]any{"problems": p}}
	}
	id, err := s.Engine.StartTest(r.Context(), ws.ID, ws.token, ws.Channels, v.ID, name, v.Spec, number, req.Values, strings.TrimSpace(req.Line))
	if errors.Is(err, dialer.ErrBusy) {
		return conflict("channels_busy", "every channel has a call in progress; wait for one to end")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"call_id": id})
	return nil
}

type callView struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	CampaignID      *string         `json:"campaign_id"`
	TargetID        *string         `json:"target_id"`
	VersionID       string          `json:"version_id"`
	AgentName       string          `json:"agent_name"`
	VersionN        int             `json:"version_n"`
	Phone           string          `json:"phone"`
	Status          string          `json:"status"`
	EndReason       string          `json:"end_reason"`
	Outcome         string          `json:"outcome"`
	DurationSeconds *float64        `json:"duration_seconds"`
	ChargedG        *float64        `json:"charged_g"`
	Checks          json.RawMessage `json:"checks"`
	Analysis        string          `json:"analysis"`
	Accepted        *bool           `json:"accepted"`
	Feedback        string          `json:"feedback"`
	Summary         string          `json:"summary"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at"`
	EndedAt         *time.Time      `json:"ended_at"`
	Settled         bool            `json:"settled"`
	HasRecording    bool            `json:"has_recording"`
	// Only on GET /api/calls/{id}.
	Transcript json.RawMessage `json:"transcript,omitempty"`
	Answers    json.RawMessage `json:"answers,omitempty"`
	Form       []model.Field   `json:"form,omitempty"`
}

const callColumns = `c.id, c.kind, c.campaign_id, c.target_id, c.agent_version_id, a.name, v.n, c.phone, c.status, c.end_reason, c.outcome, c.duration_seconds,
	c.charged_g, c.checks, c.analysis, c.accepted, c.feedback, c.summary, c.created_at, c.started_at, c.ended_at, c.settled_at is not null,
	c.status = 'ended' and c.gapi_call_id <> ''`

const callJoins = ` from calls c join agent_versions v on v.id = c.agent_version_id join agents a on a.id = v.agent_id `

func scanCall(row pgx.Row, more ...any) (callView, error) {
	c := callView{}
	dest := []any{&c.ID, &c.Kind, &c.CampaignID, &c.TargetID, &c.VersionID, &c.AgentName, &c.VersionN, &c.Phone, &c.Status, &c.EndReason, &c.Outcome, &c.DurationSeconds,
		&c.ChargedG, &c.Checks, &c.Analysis, &c.Accepted, &c.Feedback, &c.Summary, &c.CreatedAt, &c.StartedAt, &c.EndedAt, &c.Settled, &c.HasRecording}
	return c, row.Scan(append(dest, more...)...)
}

// listCalls: GET /api/calls?kind=test&campaign_id=&version_id=&limit=; needs_form=1 leaves the
// answered calls whose form nobody has filled yet.
func (s *Server) listCalls(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.Query(r.Context(), `select `+callColumns+callJoins+`where c.workspace_id = $1
		and ($2 = '' or c.kind = $2) and ($3 = '' or c.campaign_id::text = $3) and ($4 = '' or c.agent_version_id::text = $4 or v.agent_id::text = $4)
		and (not $6 or (c.status = 'ended' and c.transcript is not null and c.answers = '{}'::jsonb and c.summary = ''))
		order by c.created_at desc limit $5`, ws.ID, q.Get("kind"), q.Get("campaign_id"), q.Get("version_id"), limit, q.Get("needs_form") == "1")
	if err != nil {
		return err
	}
	defer rows.Close()
	calls := []callView{}
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return err
		}
		calls = append(calls, c)
	}
	writeJSON(w, 200, map[string]any{"calls": calls})
	return rows.Err()
}

func (s *Server) getCall(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var spec model.Spec
	var transcript, answers []byte
	c, err := scanCall(s.DB.QueryRow(r.Context(), `select `+callColumns+`, c.transcript, c.answers, v.spec`+callJoins+`where c.id = $1 and c.workspace_id = $2`,
		r.PathValue("id"), ws.ID), &transcript, &answers, &spec)
	if err != nil {
		return notFound("call")
	}
	c.Transcript, c.Answers, c.Form = transcript, answers, spec.Form
	if c.Form == nil {
		c.Form = []model.Field{}
	}
	writeJSON(w, 200, c)
	return nil
}

func (s *Server) gapiCall(r *http.Request, ws *Workspace) (string, error) {
	var id string
	if err := s.DB.QueryRow(r.Context(), `select gapi_call_id from calls where id = $1 and workspace_id = $2`, r.PathValue("id"), ws.ID).Scan(&id); err != nil || id == "" {
		return "", notFound("call")
	}
	return id, nil
}

func (s *Server) hangUp(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	id, err := s.gapiCall(r, ws)
	if err != nil {
		return err
	}
	if err := s.Gapi.End(r.Context(), ws.token, id); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"ok": true})
	return nil
}

func (s *Server) recording(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	id, err := s.gapiCall(r, ws)
	if err != nil {
		return err
	}
	if err := s.Gapi.Recording(r.Context(), ws.token, id, w, r.Header.Get("Range")); err != nil {
		return &fail{Status: http.StatusNotFound, Code: "recording_not_ready", Hint: "the recording arrives a few seconds after the call ends"}
	}
	return nil
}

// verdict: POST /api/calls/{id}/verdict {"accepted", "feedback"}: what the
// tester says of a test call. Accepting it is what lets campaigns start with
// that version of the agent; a call in which the agent did not introduce
// itself as an AI assistant cannot be accepted.
func (s *Server) verdict(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Accepted bool   `json:"accepted"`
		Feedback string `json:"feedback"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	feedback := strings.TrimSpace(req.Feedback)
	if len([]rune(feedback)) > 4000 {
		return bad("feedback_invalid", "feedback is at most 4,000 characters")
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var kind, status, versionID string
	var settled bool
	var checks map[string]any
	err = tx.QueryRow(r.Context(), `select kind, status, agent_version_id, settled_at is not null, checks from calls where id = $1 and workspace_id = $2 for update`,
		r.PathValue("id"), ws.ID).Scan(&kind, &status, &versionID, &settled, &checks)
	if err != nil {
		return notFound("call")
	}
	if kind != "test" {
		return bad("not_a_test_call", "only a test call is accepted or sent back")
	}
	if req.Accepted {
		switch {
		case !settled || status != "ended":
			return conflict("call_not_answered", "only a test call that was answered and has ended can be accepted")
		case checks["ai_disclosed"] != true:
			return conflict("agent_did_not_introduce_itself", "in this call the agent did not say it is an AI assistant in its first words; correct the script and call again")
		}
	}
	if _, err := tx.Exec(r.Context(), `update calls set accepted = $2, feedback = $3 where id = $1`, r.PathValue("id"), req.Accepted, feedback); err != nil {
		return err
	}
	if req.Accepted {
		if _, err := tx.Exec(r.Context(), `update agent_versions set accepted_at = $2 where id = $1 and accepted_at is null`, versionID, s.Now()); err != nil {
			return err
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"accepted": req.Accepted})
	return nil
}

// editAnswers: PUT /api/calls/{id}/answers {"key": value, ...}: a person's
// correction of what the call's form holds.
func (s *Server) editAnswers(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		return err
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var answers map[string]model.Answer
	var spec model.Spec
	err = tx.QueryRow(r.Context(), `select c.answers, v.spec from calls c join agent_versions v on v.id = c.agent_version_id where c.id = $1 and c.workspace_id = $2 for update of c`,
		r.PathValue("id"), ws.ID).Scan(&answers, &spec)
	if err != nil {
		return notFound("call")
	}
	if answers == nil {
		answers = map[string]model.Answer{}
	}
	known := map[string]bool{}
	for _, f := range spec.Form {
		known[f.Key] = true
	}
	for key, value := range req {
		if !known[key] {
			return bad("field_unknown", "the form has no field "+key)
		}
		if value == nil || value == "" {
			delete(answers, key)
			continue
		}
		a := answers[key]
		a.Value, a.Edited = value, true
		answers[key] = a
	}
	if _, err := tx.Exec(r.Context(), `update calls set answers = $2 where id = $1`, r.PathValue("id"), answers); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"answers": answers})
	return nil
}

// fillForm: PUT /api/calls/{id}/form {"answers": {"key": {"value", "quote"}}, "summary"}:
// the form as read from the call's text by whoever read it (an assistant over
// MCP, a person). Every answer carries the words it was taken from, and the
// words have to be in the call's text: an answer nobody said is refused.
func (s *Server) fillForm(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Answers map[string]model.Answer `json:"answers"`
		Summary *string                 `json:"summary"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var answers map[string]model.Answer
	var spec model.Spec
	var turns []struct {
		Role, Text string
	}
	var summary string
	err = tx.QueryRow(r.Context(), `select c.answers, c.summary, c.transcript, v.spec from calls c join agent_versions v on v.id = c.agent_version_id
		where c.id = $1 and c.workspace_id = $2 for update of c`, r.PathValue("id"), ws.ID).Scan(&answers, &summary, &turns, &spec)
	if err != nil {
		return notFound("call")
	}
	if len(turns) == 0 {
		return conflict("call_has_no_text", "this call has no text to read answers from")
	}
	said := make([]string, 0, len(turns))
	for _, t := range turns {
		if t.Role == "caller" || t.Role == "agent" {
			said = append(said, plain(t.Text))
		}
	}
	fields := map[string]model.Field{}
	for _, f := range spec.Form {
		fields[f.Key] = f
	}
	if answers == nil {
		answers = map[string]model.Answer{}
	}
	for key, a := range req.Answers {
		f, ok := fields[key]
		if !ok {
			return bad("field_unknown", "the form has no field "+key)
		}
		if a.Value == nil || a.Value == "" {
			delete(answers, key)
			continue
		}
		if err := fits(f, a.Value); err != nil {
			return bad("answer_invalid", key+": "+err.Error())
		}
		quote := plain(a.Quote)
		found := quote != ""
		if found {
			found = false
			for _, line := range said {
				if strings.Contains(line, quote) {
					found = true
				}
			}
		}
		if !found {
			return bad("quote_not_in_call", key+": give the words of the call the answer was taken from, exactly as they stand in its text")
		}
		answers[key] = model.Answer{Value: a.Value, Quote: strings.TrimSpace(a.Quote)}
	}
	if req.Summary != nil {
		if summary = strings.TrimSpace(*req.Summary); len([]rune(summary)) > 600 {
			return bad("summary_invalid", "a summary is at most 600 characters")
		}
	}
	// a call whose result the CRM has already heard gets its form sent after it
	if _, err := tx.Exec(r.Context(), `update calls set answers = $2, summary = $3,
		crm_state = case when crm_state in ('done', 'failed') then 'form' else crm_state end where id = $1`, r.PathValue("id"), answers, summary); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"answers": answers, "summary": summary})
	return nil
}

var plainer = strings.NewReplacer("ʻ", "'", "‘", "'", "’", "'", "`", "'", "ʼ", "'", "«", "", "»", "", "\"", "", ",", "", ".", "", "!", "", "?", "")

// plain brings words to the form two spellings of the same speech share.
func plain(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(plainer.Replace(s))), " ")
}

// fits checks an answer's value against its field's type.
func fits(f model.Field, v any) error {
	switch f.Type {
	case "rating":
		if n, ok := v.(float64); !ok || n < 1 || n > 5 || n != float64(int(n)) {
			return errors.New("a rating is a whole number from 1 to 5")
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return errors.New("the value is a number")
		}
	case "yesno":
		if _, ok := v.(bool); !ok {
			return errors.New("the value is true or false")
		}
	case "choice":
		text, _ := v.(string)
		for _, o := range f.Options {
			if o == text {
				return nil
			}
		}
		return errors.New("the value is one of: " + strings.Join(f.Options, ", "))
	case "datetime":
		text, _ := v.(string)
		if _, err := time.Parse("2006-01-02T15:04", text); err != nil {
			if _, err := time.Parse("2006-01-02", text); err != nil {
				return errors.New("a date and time is written 2026-11-14T15:30, a date alone 2026-11-14")
			}
		}
	default:
		if text, ok := v.(string); !ok || len([]rune(text)) > 1000 {
			return errors.New("the value is a text of at most 1,000 characters")
		}
	}
	return nil
}
