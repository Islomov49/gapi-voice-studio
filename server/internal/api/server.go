// Package api is the Studio's HTTP API, what its web interface talks to.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Islomov49/gapi-voice-studio/server/internal/compile"
	"github.com/Islomov49/gapi-voice-studio/server/internal/dialer"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/seal"
)

type Server struct {
	DB   *pgxpool.Pool
	Gapi *gapi.Client
	Seal *seal.Sealer
	Now  func() time.Time
	// Places the calls and follows them: a test call starts through it.
	Engine *dialer.Engine
	// Cookies are marked Secure: on for anything but a developer's machine.
	SecureCookies bool
	// The built web interface, served for every path that is not the API's.
	Web http.Handler
	// A Gapi project token given to the service itself (STUDIO_GAPI_TOKEN): the
	// Studio on one's own machine then opens straight into that project, in
	// the browser and for an assistant over MCP, with nothing to paste.
	DefaultToken string
	// The service's version, as MCP clients see it.
	Version string

	inner  *http.ServeMux
	mu     sync.Mutex
	tokens map[string]tokenEntry
}

type tokenEntry struct {
	workspaceID string
	until       time.Time
}

// fail is an answer the interface shows as it is: a code it can switch on
// and a sentence for the person.
type fail struct {
	Status int
	Code   string
	Hint   string
	Extra  map[string]any
}

func (f *fail) Error() string { return f.Code + ": " + f.Hint }

func bad(code, hint string) error {
	return &fail{Status: http.StatusBadRequest, Code: code, Hint: hint}
}
func conflict(code, hint string) error {
	return &fail{Status: http.StatusConflict, Code: code, Hint: hint}
}
func notFound(what string) error {
	return &fail{Status: http.StatusNotFound, Code: "not_found", Hint: what + " not found"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var f *fail
	var g *gapi.Error
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &f):
		body := map[string]any{"error": f.Code, "hint": f.Hint}
		for k, v := range f.Extra {
			body[k] = v
		}
		writeJSON(w, f.Status, body)
	case errors.As(err, &g):
		// Gapi's own refusal, passed on with its words: the person can act on
		// "quota_exceeded" or "voice_line_not_found", not on "bad gateway".
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "gapi_" + g.Code, "hint": g.Hint})
	case errors.Is(err, pgx.ErrNoRows), errors.As(err, &pg) && pg.Code == "22P02":
		// 22P02: an id in the address that is not an id
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "hint": "not found"})
	default:
		slog.Error("api: internal error", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal", "hint": "try again later"})
	}
}

func readJSON(r *http.Request, v any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return bad("invalid_body", "the request could not be read")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return bad("invalid_body", "the request is not the JSON this endpoint takes")
	}
	return nil
}

// Workspace is who a request acts for.
type Workspace struct {
	ID          string
	Name        string
	TesterPhone string
	Channels    int
	token       string
}

type handler func(w http.ResponseWriter, r *http.Request, ws *Workspace) error

const cookieName = "studio_session"

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (s *Server) auth(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || c.Value == "" {
			if s.DefaultToken != "" {
				// the service was started with its project's token: no sign-in
				ws, err := s.workspaceForToken(r.Context(), s.DefaultToken)
				if err == nil {
					err = s.startSession(r.Context(), w, ws.ID)
				}
				if err == nil {
					err = h(w, r, ws)
				}
				if err != nil {
					writeErr(w, err)
				}
				return
			}
			writeErr(w, &fail{Status: http.StatusUnauthorized, Code: "signed_out", Hint: "connect a Gapi project first"})
			return
		}
		ws := &Workspace{}
		var sealed []byte
		err = s.DB.QueryRow(r.Context(), `select w.id, w.name, w.tester_phone, w.channels, w.gapi_token_sealed
			from sessions s join workspaces w on w.id = s.workspace_id where s.token_hash = $1 and s.expires_at > $2`,
			hashToken(c.Value), s.Now()).Scan(&ws.ID, &ws.Name, &ws.TesterPhone, &ws.Channels, &sealed)
		if err != nil {
			writeErr(w, &fail{Status: http.StatusUnauthorized, Code: "signed_out", Hint: "the session has ended; connect the project again"})
			return
		}
		if ws.token, err = s.Seal.Open(sealed); err != nil {
			writeErr(w, &fail{Status: http.StatusUnauthorized, Code: "signed_out", Hint: "the project's token can no longer be read; connect the project again"})
			return
		}
		if err := h(w, r, ws); err != nil {
			writeErr(w, err)
		}
	}
}

func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, workspaceID string) error {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := hex.EncodeToString(raw)
	expires := s.Now().Add(30 * 24 * time.Hour)
	if _, err := s.DB.Exec(ctx, `insert into sessions (token_hash, workspace_id, expires_at) values ($1, $2, $3)`, hashToken(token), workspaceID, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Expires: expires, HttpOnly: true, Secure: s.SecureCookies, SameSite: http.SameSiteLaxMode})
	return nil
}

// limits reads Gapi's limits for the project, falling back to the known ones
// when Gapi does not answer: a check of a script should not wait on it.
func (s *Server) limits(ctx context.Context, ws *Workspace) compile.Limits {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	o, err := s.Gapi.Options(ctx, ws.token)
	if err != nil || o.Limits.PromptChars == 0 {
		return compile.DefaultLimits
	}
	return compile.Limits{PromptChars: o.Limits.PromptChars, Variables: o.Limits.Variables, VocabularyTerms: o.Limits.VocabularyTerms, MaxCallSeconds: o.Limits.MaxDurationSeconds}
}

// routes is every endpoint that acts for a workspace: the interface reaches
// them with its session cookie, the MCP server (mcp.go) from inside.
func (s *Server) routes() map[string]handler {
	return map[string]handler{
		"GET /api/me":        s.me,
		"PUT /api/workspace": s.updateWorkspace,

		"GET /api/templates":             s.listTemplates,
		"GET /api/agents":                s.listAgents,
		"POST /api/agents":               s.createAgent,
		"GET /api/agents/{id}":           s.getAgent,
		"PUT /api/agents/{id}":           s.renameAgent,
		"DELETE /api/agents/{id}":        s.archiveAgent,
		"POST /api/agents/{id}/versions": s.createVersion,
		"POST /api/check":                s.checkSpec,

		"POST /api/calls/test":          s.testCall,
		"GET /api/calls":                s.listCalls,
		"GET /api/calls/{id}":           s.getCall,
		"DELETE /api/calls/{id}":        s.hangUp,
		"GET /api/calls/{id}/recording": s.recording,
		"POST /api/calls/{id}/verdict":  s.verdict,
		"PUT /api/calls/{id}/answers":   s.editAnswers,
		"PUT /api/calls/{id}/form":      s.fillForm,
		"POST /api/calls/{id}/analyze":  s.analyzeCall,

		"GET /api/ai":    s.getAI,
		"PUT /api/ai":    s.setAI,
		"DELETE /api/ai": s.dropAI,

		"GET /api/lists":         s.listLists,
		"POST /api/lists":        s.uploadList,
		"POST /api/lists/rows":   s.uploadRows,
		"GET /api/lists/{id}":    s.getList,
		"DELETE /api/lists/{id}": s.archiveList,

		"GET /api/campaigns":                  s.listCampaigns,
		"POST /api/campaigns":                 s.createCampaign,
		"GET /api/campaigns/{id}":             s.getCampaign,
		"PUT /api/campaigns/{id}":             s.updateCampaign,
		"POST /api/campaigns/{id}/start":      s.startCampaign,
		"POST /api/campaigns/{id}/pause":      s.pauseCampaign,
		"GET /api/campaigns/{id}/targets":     s.listTargets,
		"GET /api/campaigns/{id}/export.xlsx": s.exportCampaign,

		"GET /api/crm":                s.listCRM,
		"POST /api/crm":               s.connectCRM,
		"DELETE /api/crm/{id}":        s.disconnectCRM,
		"GET /api/crm/{id}/pipelines": s.crmPipelines,
		"POST /api/crm/{id}/import":   s.crmImport,

		"GET /api/stoplist":            s.listStop,
		"POST /api/stoplist":           s.addStop,
		"DELETE /api/stoplist/{phone}": s.removeStop,
	}
}

type workspaceKey struct{}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DB.Ping(r.Context()); err != nil {
			http.Error(w, "database", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /api/connect", s.connect)
	mux.HandleFunc("POST /api/logout", s.logout)
	// The same endpoints once more, for calls from inside the process: the
	// workspace comes with the request's context instead of a cookie.
	s.inner = http.NewServeMux()
	for pattern, h := range s.routes() {
		mux.HandleFunc(pattern, s.auth(h))
		s.inner.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := h(w, r, r.Context().Value(workspaceKey{}).(*Workspace)); err != nil {
				writeErr(w, err)
			}
		})
	}
	mux.HandleFunc("/mcp", s.mcpHTTP)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, notFound("endpoint"))
	})
	if s.Web != nil {
		mux.Handle("/", s.Web)
	}
	return mux
}

// tokenKey is what tells one Gapi project's token from another's without
// keeping the token readable: the public id inside it ("gapi_ab12cd34_...")
// or, for a token of another shape, a hash.
func tokenKey(token string) string {
	if parts := strings.SplitN(token, "_", 3); len(parts) == 3 && parts[0] == "gapi" && len(parts[1]) >= 6 {
		return "token:" + parts[1]
	}
	return "hash:" + hex.EncodeToString(hashToken(token)[:12])
}

// workspaceForToken is the workspace of a Gapi project token: the token is
// checked against Gapi (and remembered for a few minutes), kept sealed, and
// its workspace made if this is the first time it is seen.
func (s *Server) workspaceForToken(ctx context.Context, token string) (*Workspace, error) {
	token = strings.TrimSpace(token)
	if len(token) < 20 || strings.ContainsAny(token, " \n\t") {
		return nil, bad("token_invalid", "paste the project's API token from the Gapi cabinet, page \"Tokens\"")
	}
	key := hex.EncodeToString(hashToken(token))
	s.mu.Lock()
	entry, known := s.tokens[key]
	s.mu.Unlock()
	if !known || s.Now().After(entry.until) {
		if _, err := s.Gapi.Options(ctx, token); err != nil {
			switch gapi.Code(err) {
			case "voice_not_available":
				return nil, &fail{Status: http.StatusForbidden, Code: "voice_not_available", Hint: "voice agents are not open for this Gapi account yet"}
			case "":
				return nil, &fail{Status: http.StatusBadGateway, Code: "gapi_unreachable", Hint: "Gapi did not answer; try again"}
			default:
				return nil, &fail{Status: http.StatusUnauthorized, Code: "token_refused", Hint: "Gapi did not accept this token"}
			}
		}
		var id string
		if err := s.DB.QueryRow(ctx, `insert into workspaces (gapi_key, gapi_token_sealed) values ($1, $2)
			on conflict (gapi_key) do update set gapi_token_sealed = excluded.gapi_token_sealed returning id`, tokenKey(token), s.Seal.Seal(token)).Scan(&id); err != nil {
			return nil, err
		}
		entry = tokenEntry{workspaceID: id, until: s.Now().Add(5 * time.Minute)}
		s.mu.Lock()
		if s.tokens == nil {
			s.tokens = map[string]tokenEntry{}
		}
		s.tokens[key] = entry
		s.mu.Unlock()
	}
	ws := &Workspace{ID: entry.workspaceID, token: token}
	err := s.DB.QueryRow(ctx, `select name, tester_phone, channels from workspaces where id = $1`, ws.ID).Scan(&ws.Name, &ws.TesterPhone, &ws.Channels)
	return ws, err
}
