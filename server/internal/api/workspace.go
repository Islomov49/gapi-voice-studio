package api

import (
	"net/http"
	"strings"

	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/phone"
)

// connect: POST /api/connect {"token": "<a Gapi project token>"}: the token
// is checked against Gapi, kept sealed, and a session starts.
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	ws, err := s.workspaceForToken(r.Context(), req.Token)
	if err != nil {
		writeErr(w, err)
		return
	}
	id := ws.ID
	if err := s.startSession(r.Context(), w, id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_, _ = s.DB.Exec(r.Context(), `delete from sessions where token_hash = $1`, hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.SecureCookies, SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// me: the workspace and how the project stands in Gapi right now (voices,
// lines, the price of a minute); what is missing for a call is said in "needs".
func (s *Server) me(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	out := map[string]any{
		"workspace": map[string]any{"id": ws.ID, "name": ws.Name, "tester_phone": ws.TesterPhone, "channels": ws.Channels},
	}
	var needs []string
	o, err := s.Gapi.Options(r.Context(), ws.token)
	if err != nil {
		out["gapi_error"] = gapi.Code(err)
		needs = append(needs, "gapi")
	} else {
		out["voices"], out["g_per_minute"], out["online"], out["limits"] = o.Voices, o.GPerMinute, o.Online, o.Limits
		if !o.Online {
			needs = append(needs, "runner")
		}
	}
	lines, err := s.Gapi.Lines(r.Context(), ws.token)
	if err == nil {
		if lines == nil {
			lines = []gapi.Line{}
		}
		out["lines"] = lines
		if len(lines) == 0 {
			needs = append(needs, "line")
		}
	}
	if ws.TesterPhone == "" {
		needs = append(needs, "tester_phone")
	}
	var open int
	if err := s.DB.QueryRow(r.Context(), `select count(*) from calls where workspace_id = $1 and settled_at is null`, ws.ID).Scan(&open); err != nil {
		return err
	}
	var ai bool
	if err := s.DB.QueryRow(r.Context(), `select gemini_key_sealed is not null from workspaces where id = $1`, ws.ID).Scan(&ai); err != nil {
		return err
	}
	// ai: the workspace keeps its person's Gemini key, and the Studio reads each answered call itself
	out["open_calls"], out["needs"], out["ai"] = open, needs, ai
	writeJSON(w, 200, out)
	return nil
}

func (s *Server) updateWorkspace(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Name        *string `json:"name"`
		TesterPhone *string `json:"tester_phone"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if len([]rune(name)) > 80 {
			return bad("name_invalid", "a name is at most 80 characters")
		}
		ws.Name = name
	}
	if req.TesterPhone != nil {
		ws.TesterPhone = ""
		if strings.TrimSpace(*req.TesterPhone) != "" {
			number, ok := phone.Normalize(*req.TesterPhone)
			if !ok {
				return bad("phone_invalid", "write the number as +998901234567")
			}
			ws.TesterPhone = number
		}
	}
	if _, err := s.DB.Exec(r.Context(), `update workspaces set name = $2, tester_phone = $3 where id = $1`, ws.ID, ws.Name, ws.TesterPhone); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"name": ws.Name, "tester_phone": ws.TesterPhone})
	return nil
}
