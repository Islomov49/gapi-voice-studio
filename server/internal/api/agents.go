package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Islomov49/gapi-voice-studio/server/internal/compile"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
	"github.com/Islomov49/gapi-voice-studio/server/internal/templates"
)

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	writeJSON(w, 200, map[string]any{"templates": templates.All})
	return nil
}

type versionView struct {
	ID         string     `json:"id"`
	N          int        `json:"n"`
	Author     string     `json:"author"`
	Note       string     `json:"note"`
	AcceptedAt *time.Time `json:"accepted_at"`
	CreatedAt  time.Time  `json:"created_at"`
	Spec       model.Spec `json:"spec"`
}

type agentView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Template  string    `json:"template"`
	CreatedAt time.Time `json:"created_at"`
	// The newest version: what the editor opens and a test call uses.
	Latest *versionView `json:"latest,omitempty"`
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	rows, err := s.DB.Query(r.Context(), `select a.id, a.name, a.template, a.created_at, v.id, v.n, v.author, v.note, v.accepted_at, v.created_at, v.spec
		from agents a join lateral (select * from agent_versions where agent_id = a.id order by n desc limit 1) v on true
		where a.workspace_id = $1 and a.archived_at is null order by a.created_at desc`, ws.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	agents := []agentView{}
	for rows.Next() {
		a, v := agentView{}, versionView{}
		if err := rows.Scan(&a.ID, &a.Name, &a.Template, &a.CreatedAt, &v.ID, &v.N, &v.Author, &v.Note, &v.AcceptedAt, &v.CreatedAt, &v.Spec); err != nil {
			return err
		}
		a.Latest = &v
		agents = append(agents, a)
	}
	writeJSON(w, 200, map[string]any{"agents": agents})
	return rows.Err()
}

func cleanSpec(spec *model.Spec) {
	spec.Company, spec.Script = strings.TrimSpace(spec.Company), strings.TrimSpace(spec.Script)
	if spec.Voice == "" {
		spec.Voice = "charos"
	}
	if spec.MaxCallSeconds == 0 {
		spec.MaxCallSeconds = 300
	}
	var words []string
	for _, v := range spec.Vocabulary {
		if v = strings.TrimSpace(v); v != "" {
			words = append(words, v)
		}
	}
	spec.Vocabulary = words
	for i := range spec.Form {
		f := &spec.Form[i]
		f.Key, f.Title, f.Ask = strings.TrimSpace(f.Key), strings.TrimSpace(f.Title), strings.TrimSpace(f.Ask)
	}
}

// createAgent: POST /api/agents {"name", "template"} or {"name", "spec"}.
func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Name     string      `json:"name"`
		Template string      `json:"template"`
		Spec     *model.Spec `json:"spec"`
		Author   string      `json:"author"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	spec := model.Spec{}
	switch {
	case req.Spec != nil:
		spec = *req.Spec
	case req.Template != "":
		t, ok := templates.ByID(req.Template)
		if !ok {
			return bad("template_unknown", "template is one of GET /api/templates")
		}
		spec = t.Spec
		if req.Name == "" {
			req.Name = t.Title
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 80 {
		return bad("name_invalid", "an agent has a name of at most 80 characters")
	}
	cleanSpec(&spec)
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var id string
	if err := tx.QueryRow(r.Context(), `insert into agents (workspace_id, name, template) values ($1, $2, $3) returning id`, ws.ID, name, req.Template).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.Exec(r.Context(), `insert into agent_versions (agent_id, n, spec, author) values ($1, 1, $2, $3)`, id, spec, author(req.Author)); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	return s.writeAgent(r.Context(), w, http.StatusCreated, ws, id)
}

func author(a string) string {
	if a == "assistant" {
		return a
	}
	return "user"
}

func (s *Server) writeAgent(ctx context.Context, w http.ResponseWriter, status int, ws *Workspace, id string) error {
	a := agentView{}
	if err := s.DB.QueryRow(ctx, `select id, name, template, created_at from agents where id = $1 and workspace_id = $2 and archived_at is null`, id, ws.ID).
		Scan(&a.ID, &a.Name, &a.Template, &a.CreatedAt); err != nil {
		return notFound("agent")
	}
	rows, err := s.DB.Query(ctx, `select id, n, author, note, accepted_at, created_at, spec from agent_versions where agent_id = $1 order by n desc`, id)
	if err != nil {
		return err
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (versionView, error) {
		v := versionView{}
		return v, row.Scan(&v.ID, &v.N, &v.Author, &v.Note, &v.AcceptedAt, &v.CreatedAt, &v.Spec)
	})
	if err != nil || len(versions) == 0 {
		return notFound("agent")
	}
	latest := versions[0]
	lim := s.limits(ctx, ws)
	writeJSON(w, status, map[string]any{
		"agent": a, "latest": latest, "versions": versions,
		"problems":  problems(latest.Spec, lim),
		"variables": compile.Variables(latest.Spec),
		"prompt":    compile.Preview(latest.Spec, nil),
	})
	return nil
}

func problems(spec model.Spec, lim compile.Limits) []compile.Problem {
	if p := compile.Check(spec, lim); p != nil {
		return p
	}
	return []compile.Problem{}
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	return s.writeAgent(r.Context(), w, 200, ws, r.PathValue("id"))
}

func (s *Server) renameAgent(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 80 {
		return bad("name_invalid", "an agent has a name of at most 80 characters")
	}
	tag, err := s.DB.Exec(r.Context(), `update agents set name = $3 where id = $1 and workspace_id = $2 and archived_at is null`, r.PathValue("id"), ws.ID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFound("agent")
	}
	writeJSON(w, 200, map[string]any{"name": name})
	return nil
}

func (s *Server) archiveAgent(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var busy bool
	if err := s.DB.QueryRow(r.Context(), `select exists (select 1 from campaigns c join agent_versions v on v.id = c.agent_version_id
		where v.agent_id = $1 and c.workspace_id = $2 and c.status = 'running')`, r.PathValue("id"), ws.ID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return conflict("agent_in_use", "a running campaign calls with this agent; pause it first")
	}
	tag, err := s.DB.Exec(r.Context(), `update agents set archived_at = $3 where id = $1 and workspace_id = $2 and archived_at is null`, r.PathValue("id"), ws.ID, s.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFound("agent")
	}
	writeJSON(w, 200, map[string]any{"archived": true})
	return nil
}

// createVersion: POST /api/agents/{id}/versions {"spec", "note", "author"}.
// A change of the script or the form is a new version, which no test call
// has been accepted for yet; saving what the newest version already says
// makes nothing.
func (s *Server) createVersion(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Spec   model.Spec `json:"spec"`
		Note   string     `json:"note"`
		Author string     `json:"author"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	cleanSpec(&req.Spec)
	id := r.PathValue("id")
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var n int
	var raw []byte
	err = tx.QueryRow(r.Context(), `select v.n, v.spec from agent_versions v join agents a on a.id = v.agent_id
		where a.id = $1 and a.workspace_id = $2 and a.archived_at is null order by v.n desc limit 1 for update of a`, id, ws.ID).Scan(&n, &raw)
	if err != nil {
		return notFound("agent")
	}
	var current model.Spec
	_ = json.Unmarshal(raw, &current)
	cleanSpec(&current)
	if !reflect.DeepEqual(normal(current), normal(req.Spec)) {
		note := strings.TrimSpace(req.Note)
		if len([]rune(note)) > 500 {
			note = string([]rune(note)[:500])
		}
		if _, err := tx.Exec(r.Context(), `insert into agent_versions (agent_id, n, spec, author, note) values ($1, $2, $3, $4, $5)`, id, n+1, req.Spec, author(req.Author), note); err != nil {
			return err
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	return s.writeAgent(r.Context(), w, 200, ws, id)
}

// normal makes two specs that say the same compare equal: a nil list and an
// empty one are the same thing to a person.
func normal(s model.Spec) model.Spec {
	if len(s.Variables) == 0 {
		s.Variables = nil
	}
	if len(s.Vocabulary) == 0 {
		s.Vocabulary = nil
	}
	if len(s.Form) == 0 {
		s.Form = nil
	}
	for i := range s.Form {
		if len(s.Form[i].Options) == 0 {
			s.Form[i].Options = nil
		}
	}
	return s
}

// checkSpec: POST /api/check {"spec", "values"}: what keeps a script from
// being called with, and the instruction as the model would read it. Nothing
// is saved.
func (s *Server) checkSpec(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Spec   model.Spec        `json:"spec"`
		Values map[string]string `json:"values"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	cleanSpec(&req.Spec)
	writeJSON(w, 200, map[string]any{
		"problems":  problems(req.Spec, s.limits(r.Context(), ws)),
		"variables": compile.Variables(req.Spec),
		"prompt":    compile.Preview(req.Spec, req.Values),
	})
	return nil
}

// version loads one version of one of the workspace's agents.
func (s *Server) version(ctx context.Context, ws *Workspace, id string) (agentName string, v versionView, err error) {
	err = s.DB.QueryRow(ctx, `select a.name, v.id, v.n, v.author, v.note, v.accepted_at, v.created_at, v.spec from agent_versions v
		join agents a on a.id = v.agent_id where v.id = $1 and a.workspace_id = $2 and a.archived_at is null`, id, ws.ID).
		Scan(&agentName, &v.ID, &v.N, &v.Author, &v.Note, &v.AcceptedAt, &v.CreatedAt, &v.Spec)
	if err != nil {
		err = notFound("agent version")
	}
	return
}
