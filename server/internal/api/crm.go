package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Islomov49/gapi-voice-studio/server/internal/crm"
	"github.com/Islomov49/gapi-voice-studio/server/internal/phone"
	"github.com/Islomov49/gapi-voice-studio/server/internal/sheets"
)

type crmConnection struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Base      string    `json:"base"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) listCRM(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	rows, err := s.DB.Query(r.Context(), `select id, kind, name, base, created_at from crm_connections where workspace_id = $1 order by created_at`, ws.ID)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmConnection, error) {
		c := crmConnection{}
		return c, row.Scan(&c.ID, &c.Kind, &c.Name, &c.Base, &c.CreatedAt)
	})
	if err != nil {
		return err
	}
	if list == nil {
		list = []crmConnection{}
	}
	writeJSON(w, 200, map[string]any{"connections": list})
	return nil
}

// connectCRM: POST /api/crm {"kind": "bitrix24", "webhook": "https://<portal>/rest/<user>/<secret>/"}
// or {"kind": "amocrm", "account": "mycompany.amocrm.ru", "token": "<long-lived token>"}.
// The credentials are tried against the CRM before they are kept, sealed.
func (s *Server) connectCRM(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Kind    string `json:"kind"`
		Webhook string `json:"webhook"`
		Account string `json:"account"`
		Token   string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	var base, secret string
	var err error
	switch req.Kind {
	case "bitrix24":
		if base, secret, err = crm.SplitBitrixWebhook(req.Webhook); err != nil {
			return bad("crm_invalid", err.Error())
		}
	case "amocrm":
		if base, err = crm.AmoBase(req.Account); err != nil {
			return bad("crm_invalid", err.Error())
		}
		if secret = strings.TrimSpace(req.Token); len(secret) < 20 {
			return bad("crm_invalid", "paste the integration's long-lived token")
		}
	default:
		return bad("crm_invalid", "kind is bitrix24 or amocrm")
	}
	conn, err := crm.Open(req.Kind, base, secret)
	if err != nil {
		return err
	}
	account, err := conn.Account(r.Context())
	if err != nil {
		return &fail{Status: http.StatusBadGateway, Code: "crm_refused", Hint: err.Error()}
	}
	c := crmConnection{Kind: req.Kind, Name: account, Base: base}
	if err := s.DB.QueryRow(r.Context(), `insert into crm_connections (workspace_id, kind, name, base, secret_sealed) values ($1, $2, $3, $4, $5) returning id, created_at`,
		ws.ID, c.Kind, c.Name, c.Base, s.Seal.Seal(secret)).Scan(&c.ID, &c.CreatedAt); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, c)
	return nil
}

func (s *Server) disconnectCRM(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var busy bool
	if err := s.DB.QueryRow(r.Context(), `select exists (select 1 from campaigns where workspace_id = $1 and status = 'running' and crm->>'connection_id' = $2)`,
		ws.ID, r.PathValue("id")).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return conflict("crm_in_use", "a running campaign writes to this CRM; pause it first")
	}
	tag, err := s.DB.Exec(r.Context(), `delete from crm_connections where id = $1 and workspace_id = $2`, r.PathValue("id"), ws.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFound("connection")
	}
	writeJSON(w, 200, map[string]any{"removed": true})
	return nil
}

// connector opens one of the workspace's CRM connections.
func (s *Server) connector(ctx context.Context, ws *Workspace, id string) (crm.Connector, string, error) {
	var kind, base string
	var sealed []byte
	if err := s.DB.QueryRow(ctx, `select kind, base, secret_sealed from crm_connections where id = $1 and workspace_id = $2`, id, ws.ID).Scan(&kind, &base, &sealed); err != nil {
		return nil, "", notFound("CRM connection")
	}
	secret, err := s.Seal.Open(sealed)
	if err != nil {
		return nil, "", conflict("crm_reconnect", "the connection's secret can no longer be read; connect the CRM again")
	}
	conn, err := crm.Open(kind, base, secret)
	return conn, kind, err
}

// crmPipelines: GET /api/crm/{id}/pipelines: the funnels and their stages.
func (s *Server) crmPipelines(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	conn, _, err := s.connector(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		return err
	}
	pipelines, err := conn.Pipelines(r.Context())
	if err != nil {
		return &fail{Status: http.StatusBadGateway, Code: "crm_refused", Hint: err.Error()}
	}
	writeJSON(w, 200, map[string]any{"pipelines": pipelines})
	return nil
}

// crmImport: POST /api/crm/{id}/import {"entity", "pipeline_id", "stage_id", "name"}:
// the cards standing at a stage now, as a list of people. Each remembers its
// card, so a campaign over the list can write to it.
func (s *Server) crmImport(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Entity     string `json:"entity"`
		PipelineID string `json:"pipeline_id"`
		StageID    string `json:"stage_id"`
		Name       string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.StageID == "" || (req.Entity != "deal" && req.Entity != "lead") {
		return bad("crm_invalid", "entity is deal or lead, and stage_id one of GET /api/crm/{id}/pipelines")
	}
	conn, kind, err := s.connector(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		return err
	}
	cards, err := conn.Cards(r.Context(), req.Entity, req.PipelineID, req.StageID, sheets.MaxRows)
	if err != nil {
		return &fail{Status: http.StatusBadGateway, Code: "crm_refused", Hint: err.Error()}
	}
	stats := sheets.Stats{Rows: len(cards)}
	seen := map[string]bool{}
	var contacts []sheets.Contact
	for i, card := range cards {
		number, ok := phone.Normalize(card.Phone)
		switch {
		case !ok:
			stats.Invalid++
			continue
		case seen[number]:
			stats.Duplicates++
			continue
		}
		seen[number] = true
		contacts = append(contacts, sheets.Contact{Row: i + 1, Phone: number, Data: crm.Variables(card), Ref: crm.Ref(kind, card.Entity, card.ID)})
	}
	stats.Accepted = len(contacts)
	if len(contacts) == 0 {
		return conflict("crm_stage_empty", "no card at this stage has a person with a phone number")
	}
	var columns []sheets.Column
	for _, key := range crm.Columns(cards) {
		columns = append(columns, sheets.NewColumn(key, key))
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "CRM " + time.Now().In(time.FixedZone("", 5*3600)).Format("02.01 15:04")
	}
	source := map[string]any{"connection_id": r.PathValue("id"), "entity": req.Entity, "pipeline_id": req.PipelineID, "stage_id": req.StageID}
	out, err := s.saveList(r.Context(), ws, name, columns, contacts, stats, "CRM", source)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}

// checkCRMRules refuses rules that name a connection of another workspace or
// a stage without an id.
func (s *Server) checkCRMRules(ctx context.Context, ws *Workspace, rules *crm.Rules) error {
	if rules == nil {
		return nil
	}
	var ok bool
	if err := s.DB.QueryRow(ctx, `select exists (select 1 from crm_connections where id::text = $1 and workspace_id = $2)`, rules.ConnectionID, ws.ID).Scan(&ok); err != nil || !ok {
		return bad("crm_invalid", "crm.connection_id is one of GET /api/crm")
	}
	for result, to := range rules.Move {
		if to.StageID == "" {
			return bad("crm_invalid", "crm.move."+result+" needs a stage_id")
		}
	}
	for result, text := range rules.Task {
		if len([]rune(text)) > 500 {
			return bad("crm_invalid", "crm.task."+result+" is at most 500 characters")
		}
	}
	if f := rules.Feed; f != nil && (f.StageID == "" || (f.Entity != "deal" && f.Entity != "lead")) {
		return bad("crm_invalid", "crm.feed needs entity (deal or lead) and stage_id")
	}
	return nil
}
