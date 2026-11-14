package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/Islomov49/gapi-voice-studio/server/internal/dialer"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gemini"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
)

// The workspace's own Gemini key is optional. With it the Studio reads every
// answered call itself: fills the form, writes a summary, and says what a
// person should check by ear. The call's text then goes to Google; without a
// key nothing leaves the machine but the calls themselves.

func (s *Server) aiState(r *http.Request, ws *Workspace) (connected bool, modelName string, sealed []byte, err error) {
	err = s.DB.QueryRow(r.Context(), `select gemini_key_sealed, gemini_model from workspaces where id = $1`, ws.ID).Scan(&sealed, &modelName)
	if modelName == "" {
		modelName = gemini.DefaultModel
	}
	return len(sealed) > 0, modelName, sealed, err
}

// getAI: GET /api/ai: whether a key is kept, and the model calls are read with.
func (s *Server) getAI(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	connected, modelName, _, err := s.aiState(r, ws)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"connected": connected, "model": modelName, "default_model": gemini.DefaultModel})
	return nil
}

// setAI: PUT /api/ai {"key", "model"}: keeps the person's Gemini key, sealed,
// after Google has taken it. "key" may be left out to change the model only.
func (s *Server) setAI(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Key   string `json:"key"`
		Model string `json:"model"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	key := strings.TrimSpace(req.Key)
	modelName := strings.TrimPrefix(strings.TrimSpace(req.Model), "models/")
	connected, _, sealed, err := s.aiState(r, ws)
	if err != nil {
		return err
	}
	if key == "" {
		if !connected {
			return bad("ai_key_missing", "give a Gemini API key")
		}
		if key, err = s.Seal.Open(sealed); err != nil {
			return bad("ai_key_missing", "the stored key can no longer be read; give it again")
		}
	} else {
		sealed = s.Seal.Seal(key)
	}
	if len(key) < 20 || len(key) > 200 || strings.ContainsAny(key, " \t\r\n") {
		return bad("ai_key_invalid", "this does not look like a Gemini API key")
	}
	if len(modelName) > 80 {
		return bad("ai_model_unknown", "a model's name is at most 80 characters")
	}
	models, err := s.Engine.Gemini.Models(r.Context(), key)
	switch code := gemini.Code(err); {
	case code == "authentication":
		return bad("ai_key_invalid", "Google does not take this key")
	case err != nil:
		return &fail{Status: http.StatusBadGateway, Code: "ai_unreachable", Hint: "Google's API did not answer; try again"}
	}
	if modelName != "" && len(models) > 0 && !slices.Contains(models, modelName) {
		flash := []string{}
		for _, m := range models {
			if strings.Contains(m, "flash") && len(flash) < 8 {
				flash = append(flash, m)
			}
		}
		return bad("ai_model_unknown", "this key has no model "+modelName+"; it has: "+strings.Join(flash, ", "))
	}
	if _, err := s.DB.Exec(r.Context(), `update workspaces set gemini_key_sealed = $2, gemini_model = $3 where id = $1`, ws.ID, sealed, modelName); err != nil {
		return err
	}
	if modelName == "" {
		modelName = gemini.DefaultModel
	}
	writeJSON(w, 200, map[string]any{"connected": true, "model": modelName, "default_model": gemini.DefaultModel})
	return nil
}

// dropAI: DELETE /api/ai: forgets the key.
func (s *Server) dropAI(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	if _, err := s.DB.Exec(r.Context(), `update workspaces set gemini_key_sealed = null, gemini_model = '' where id = $1`, ws.ID); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"connected": false, "model": gemini.DefaultModel, "default_model": gemini.DefaultModel})
	return nil
}

// analyzeCall: POST /api/calls/{id}/analyze: the model reads a finished call
// again. Answers a person corrected stay; the rest are replaced. It changes
// what is known of the call, not whom the campaign calls next.
func (s *Server) analyzeCall(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	connected, modelName, sealed, err := s.aiState(r, ws)
	if err != nil {
		return err
	}
	if !connected {
		return conflict("ai_not_connected", "no Gemini key is kept: a person adds one in the interface, page \"Обзор\"")
	}
	var spec model.Spec
	var turns []gapi.Turn
	var answers map[string]model.Answer
	var status string
	err = s.DB.QueryRow(r.Context(), `select c.status, c.transcript, c.answers, v.spec from calls c join agent_versions v on v.id = c.agent_version_id
		where c.id = $1 and c.workspace_id = $2 and c.settled_at is not null`, r.PathValue("id"), ws.ID).Scan(&status, &turns, &answers, &spec)
	if err != nil {
		return notFound("call")
	}
	if status != "ended" || len(turns) == 0 {
		return conflict("call_has_no_text", "this call has no text to read")
	}
	reading, err := s.Engine.Read(r.Context(), sealed, modelName, spec, turns, s.Now())
	if err != nil {
		code := dialer.ReadingError(err)
		_, _ = s.DB.Exec(r.Context(), `update calls set analysis_error = $2 where id = $1`, r.PathValue("id"), code)
		return &fail{Status: http.StatusBadGateway, Code: "ai_failed", Hint: "the model did not read the call: " + code}
	}
	for key, a := range answers {
		if a.Edited {
			reading.Answers[key] = a
		}
	}
	// a call whose result the CRM has already heard gets its form sent after it
	if _, err := s.DB.Exec(r.Context(), `update calls set answers = $2, summary = $3, doubts = $4, analysis = 'ai', analysis_error = '',
		crm_state = case when crm_state in ('done', 'failed') then 'form' else crm_state end where id = $1 and workspace_id = $5`,
		r.PathValue("id"), reading.Answers, reading.Summary, reading.Doubts, ws.ID); err != nil {
		return err
	}
	return s.getCall(w, r, ws)
}
