package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Islomov49/gapi-voice-studio/server/internal/compile"
	"github.com/Islomov49/gapi-voice-studio/server/internal/crm"
	"github.com/Islomov49/gapi-voice-studio/server/internal/dialer"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
	"github.com/Islomov49/gapi-voice-studio/server/internal/phone"
	"github.com/Islomov49/gapi-voice-studio/server/internal/rules"
	"github.com/Islomov49/gapi-voice-studio/server/internal/sheets"
)

type campaignView struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	VersionID   string      `json:"version_id"`
	AgentID     string      `json:"agent_id"`
	AgentName   string      `json:"agent_name"`
	VersionN    int         `json:"version_n"`
	Accepted    bool        `json:"version_accepted"`
	ListID      *string     `json:"list_id"`
	ListName    *string     `json:"list_name"`
	Line        string      `json:"line"`
	Rules       model.Rules `json:"rules"`
	Status      string      `json:"status"`
	PauseReason string      `json:"pause_reason"`
	Portion     int         `json:"portion"`
	CreatedAt   time.Time   `json:"created_at"`
	StartedAt   *time.Time  `json:"started_at"`
	FinishedAt  *time.Time  `json:"finished_at"`
	// People by where they stand: total, waiting, calling, talked, not reached, skipped.
	Counts map[string]int `json:"counts"`
	// What the campaign does in a CRM, if anything.
	CRM *crm.Rules `json:"crm"`
	// Calls whose result could not be written to the CRM.
	CRMFailed int `json:"crm_failed"`
}

const campaignColumns = `c.id, c.name, c.agent_version_id, a.id, a.name, v.n, v.accepted_at is not null, c.list_id, l.name, c.line, c.rules, c.status, c.pause_reason,
	c.portion, c.created_at, c.started_at, c.finished_at,
	(select coalesce(jsonb_object_agg(status, n), '{}') from (select status, count(*) n from targets where campaign_id = c.id group by status) s),
	c.crm, (select count(*) from calls where campaign_id = c.id and crm_state = 'failed')`

const campaignJoins = ` from campaigns c join agent_versions v on v.id = c.agent_version_id join agents a on a.id = v.agent_id left join contact_lists l on l.id = c.list_id `

func scanCampaign(row pgx.Row) (campaignView, error) {
	c := campaignView{}
	err := row.Scan(&c.ID, &c.Name, &c.VersionID, &c.AgentID, &c.AgentName, &c.VersionN, &c.Accepted, &c.ListID, &c.ListName, &c.Line, &c.Rules, &c.Status, &c.PauseReason,
		&c.Portion, &c.CreatedAt, &c.StartedAt, &c.FinishedAt, &c.Counts, &c.CRM, &c.CRMFailed)
	return c, err
}

func (s *Server) listCampaigns(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	rows, err := s.DB.Query(r.Context(), `select `+campaignColumns+campaignJoins+`where c.workspace_id = $1 order by c.created_at desc limit 200`, ws.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	list := []campaignView{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return err
		}
		list = append(list, c)
	}
	writeJSON(w, 200, map[string]any{"campaigns": list})
	return rows.Err()
}

func (s *Server) campaign(ctx context.Context, ws *Workspace, id string) (campaignView, error) {
	c, err := scanCampaign(s.DB.QueryRow(ctx, `select `+campaignColumns+campaignJoins+`where c.id = $1 and c.workspace_id = $2`, id, ws.ID))
	if err != nil {
		return c, notFound("campaign")
	}
	return c, nil
}

// getCampaign: the campaign and what its calls have shown so far.
func (s *Server) getCampaign(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	c, err := s.campaign(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		return err
	}
	var results map[string]int
	var calls, talked int
	var seconds, charged float64
	if err := s.DB.QueryRow(r.Context(), `select
			(select coalesce(jsonb_object_agg(result, n), '{}') from (select result, count(*) n from targets where campaign_id = $1 and result <> '' group by result) s),
			count(*), count(*) filter (where status = 'ended'), coalesce(sum(duration_seconds), 0), coalesce(sum(charged_g), 0)
		from calls where campaign_id = $1`, c.ID).Scan(&results, &calls, &talked, &seconds, &charged); err != nil {
		return err
	}
	stats, err := s.formStats(r.Context(), c.ID, c.VersionID)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"campaign": c, "results": results, "form": stats,
		"calls": map[string]any{"placed": calls, "answered": talked, "seconds": seconds, "charged_g": charged}})
	return nil
}

// fieldStats is what a campaign's people answered to one field of the form.
type fieldStats struct {
	Key      string       `json:"key"`
	Title    string       `json:"title"`
	Type     string       `json:"type"`
	Answered int          `json:"answered"`
	Values   []valueCount `json:"values,omitempty"`
	Average  *float64     `json:"average,omitempty"`
}

type valueCount struct {
	Value string `json:"value"`
	N     int    `json:"n"`
}

// formStats counts the answers of a campaign's people, each person by their
// last call: how many answered a field, and for the fields with few possible
// values, how many gave each.
func (s *Server) formStats(ctx context.Context, campaignID, versionID string) ([]fieldStats, error) {
	var spec model.Spec
	if err := s.DB.QueryRow(ctx, `select spec from agent_versions where id = $1`, versionID).Scan(&spec); err != nil {
		return nil, err
	}
	out := []fieldStats{}
	if len(spec.Form) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `select k.answers from targets t join calls k on k.id = t.last_call_id where t.campaign_id = $1 and k.answers <> '{}'::jsonb`, campaignID)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowTo[map[string]model.Answer])
	if err != nil {
		return nil, err
	}
	for _, f := range spec.Form {
		st := fieldStats{Key: f.Key, Title: f.Title, Type: f.Type}
		counts := map[string]int{}
		sum := 0.0
		for _, answers := range all {
			a, ok := answers[f.Key]
			if !ok || a.Value == nil {
				continue
			}
			st.Answered++
			switch v := a.Value.(type) {
			case bool:
				counts[map[bool]string{true: "да", false: "нет"}[v]]++
			case float64:
				sum += v
				counts[strconv.FormatFloat(v, 'f', -1, 64)]++
			case string:
				if f.Type == "choice" {
					counts[v]++
				}
			}
		}
		if (f.Type == "number" || f.Type == "rating") && st.Answered > 0 {
			avg := sum / float64(st.Answered)
			st.Average = &avg
		}
		// in the order a person expects: the field's options, yes before no, numbers ascending
		var order []string
		switch f.Type {
		case "choice":
			order = f.Options
		case "yesno":
			order = []string{"да", "нет"}
		case "rating":
			order = []string{"1", "2", "3", "4", "5"}
		case "number":
			if len(counts) <= 8 {
				for v := range counts {
					order = append(order, v)
				}
				sort.Slice(order, func(i, j int) bool {
					a, _ := strconv.ParseFloat(order[i], 64)
					b, _ := strconv.ParseFloat(order[j], 64)
					return a < b
				})
			}
		}
		for _, v := range order {
			if n := counts[v]; n > 0 || f.Type != "number" {
				st.Values = append(st.Values, valueCount{Value: v, N: n})
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// createCampaign: POST /api/campaigns {"name", "version_id", "list_id", "line", "rules"}.
// The campaign is a draft with one person per contact of the list; nothing is
// dialled until it is started.
func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Name      string       `json:"name"`
		VersionID string       `json:"version_id"`
		ListID    string       `json:"list_id"`
		Line      string       `json:"line"`
		Rules     *model.Rules `json:"rules"`
		CRM       *crm.Rules   `json:"crm"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if err := s.checkCRMRules(r.Context(), ws, req.CRM); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 120 {
		return bad("name_invalid", "a campaign has a name of at most 120 characters")
	}
	r2 := model.DefaultRules()
	if req.Rules != nil {
		r2 = *req.Rules
	}
	if err := rules.Check(r2); err != nil {
		return bad("rules_invalid", err.Error())
	}
	_, v, err := s.version(r.Context(), ws, req.VersionID)
	if err != nil {
		return err
	}
	// a campaign fed from a CRM stage may start with no list: its people arrive as cards do
	var list *string
	if req.ListID != "" || req.CRM == nil || req.CRM.Feed == nil {
		var listOK bool
		if err := s.DB.QueryRow(r.Context(), `select exists (select 1 from contact_lists where id::text = $1 and workspace_id = $2 and archived_at is null)`, req.ListID, ws.ID).Scan(&listOK); err != nil || !listOK {
			return notFound("list")
		}
		list = &req.ListID
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var id string
	if err := tx.QueryRow(r.Context(), `insert into campaigns (workspace_id, name, agent_version_id, list_id, line, rules, crm) values ($1, $2, $3, $4, $5, $6, $7) returning id`,
		ws.ID, name, v.ID, list, strings.TrimSpace(req.Line), r2, req.CRM).Scan(&id); err != nil {
		return err
	}
	// numbers already on the stop list are in the campaign as skipped, so the
	// results table accounts for every row of the list
	if _, err := tx.Exec(r.Context(), `insert into targets (campaign_id, contact_id, row_no, phone, vars, ref, status, result)
		select $1, k.id, k.row_no, k.phone, k.data, k.ref,
			case when st.phone is null then 'pending' else 'skipped' end, case when st.phone is null then '' else 'stop_list' end
		from contacts k left join stoplist st on st.workspace_id = $2 and st.phone = k.phone where k.list_id = $3`, id, ws.ID, list); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	c, err := s.campaign(r.Context(), ws, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"campaign": c})
	return nil
}

// updateCampaign: PUT /api/campaigns/{id} {"name", "rules", "line", "version_id"}
// while it is not running.
func (s *Server) updateCampaign(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Name      *string      `json:"name"`
		Rules     *model.Rules `json:"rules"`
		Line      *string      `json:"line"`
		VersionID *string      `json:"version_id"`
		// {"crm": null} takes the CRM rules off.
		CRM json.RawMessage `json:"crm"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	c, err := s.campaign(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		return err
	}
	if c.Status == "running" || c.Status == "done" {
		return conflict("campaign_"+c.Status, "pause the campaign to change it; a finished one is not changed")
	}
	if req.Name != nil {
		if c.Name = strings.TrimSpace(*req.Name); c.Name == "" || len([]rune(c.Name)) > 120 {
			return bad("name_invalid", "a campaign has a name of at most 120 characters")
		}
	}
	if req.Rules != nil {
		if err := rules.Check(*req.Rules); err != nil {
			return bad("rules_invalid", err.Error())
		}
		c.Rules = *req.Rules
	}
	if req.Line != nil {
		c.Line = strings.TrimSpace(*req.Line)
	}
	if req.VersionID != nil {
		_, v, err := s.version(r.Context(), ws, *req.VersionID)
		if err != nil {
			return err
		}
		c.VersionID = v.ID
	}
	if len(req.CRM) > 0 {
		c.CRM = nil
		if string(req.CRM) != "null" {
			c.CRM = &crm.Rules{}
			if err := json.Unmarshal(req.CRM, c.CRM); err != nil {
				return bad("crm_invalid", "crm is the campaign's CRM rules")
			}
			if err := s.checkCRMRules(r.Context(), ws, c.CRM); err != nil {
				return err
			}
		}
	}
	if _, err := s.DB.Exec(r.Context(), `update campaigns set name = $2, rules = $3, line = $4, agent_version_id = $5, crm = $6 where id = $1`,
		c.ID, c.Name, c.Rules, c.Line, c.VersionID, c.CRM); err != nil {
		return err
	}
	if c, err = s.campaign(r.Context(), ws, c.ID); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"campaign": c})
	return nil
}

// startCampaign: POST /api/campaigns/{id}/start, for a draft and for a paused
// one alike. It is refused unless a person has accepted a test call of the
// very version the campaign calls with.
func (s *Server) startCampaign(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	c, err := s.campaign(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		return err
	}
	switch {
	case c.Status == "running":
		return conflict("campaign_running", "the campaign is already running")
	case c.Status == "done":
		return conflict("campaign_done", "the campaign has finished")
	case !c.Accepted:
		return &fail{Status: http.StatusConflict, Code: "test_call_needed",
			Hint:  fmt.Sprintf("version %d of the agent has no accepted test call yet: call the tester and accept the call first", c.VersionN),
			Extra: map[string]any{"version_id": c.VersionID, "agent_id": c.AgentID}}
	}
	if err := rules.Check(c.Rules); err != nil {
		return bad("rules_invalid", err.Error())
	}
	_, v, err := s.version(r.Context(), ws, c.VersionID)
	if err != nil {
		return err
	}
	if p := compile.Check(v.Spec, s.limits(r.Context(), ws)); len(p) > 0 {
		return &fail{Status: http.StatusBadRequest, Code: "agent_invalid", Hint: p[0].Text, Extra: map[string]any{"problems": p}}
	}
	lines, err := s.Gapi.Lines(r.Context(), ws.token)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return conflict("line_needed", "the Gapi project has no phone line connected: connect one in the Gapi cabinet")
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	now := s.Now()
	if c.Status == "draft" {
		if err := dialer.FirstPortion(r.Context(), tx, c.ID, c.Rules); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `update campaigns set status = 'running', pause_reason = '', portion = 1, started_at = $2 where id = $1 and status = 'draft'`, c.ID, now)
	} else {
		// A person has looked at why it stopped and lets it go on: the portion's guard does not stop it again.
		_, err = tx.Exec(r.Context(), `update campaigns set status = 'running', pause_reason = '', guard_ok = portion where id = $1 and status = 'paused'`, c.ID)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	if c, err = s.campaign(r.Context(), ws, c.ID); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"campaign": c, "calling_now": rules.Open(c.Rules, now), "next_window": rules.NextOpen(c.Rules, now)})
	return nil
}

func (s *Server) pauseCampaign(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	tag, err := s.DB.Exec(r.Context(), `update campaigns set status = 'paused', pause_reason = 'user' where id = $1 and workspace_id = $2 and status = 'running'`, r.PathValue("id"), ws.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return conflict("campaign_not_running", "only a running campaign is paused")
	}
	writeJSON(w, 200, map[string]any{"paused": true})
	return nil
}

type targetView struct {
	ID       string          `json:"id"`
	Row      int             `json:"row"`
	Phone    string          `json:"phone"`
	Vars     json.RawMessage `json:"vars"`
	Portion  int             `json:"portion"`
	Status   string          `json:"status"`
	Attempts int             `json:"attempts"`
	NextAt   *time.Time      `json:"next_at"`
	Result   string          `json:"result"`
	CallID   *string         `json:"call_id"`
	Seconds  *float64        `json:"duration_seconds"`
	Summary  *string         `json:"summary"`
	Answers  json.RawMessage `json:"answers"`
	// The person's card in a CRM, or the id an assistant gave with the row.
	Ref string `json:"ref,omitempty"`
}

const targetQuery = `select t.id, t.row_no, t.phone, t.vars, t.portion, t.status, t.attempts, t.next_at, t.result, t.last_call_id, k.duration_seconds, k.summary, k.answers, t.ref
	from targets t left join calls k on k.id = t.last_call_id where t.campaign_id = $1 `

func scanTarget(row pgx.CollectableRow) (targetView, error) {
	t := targetView{}
	return t, row.Scan(&t.ID, &t.Row, &t.Phone, &t.Vars, &t.Portion, &t.Status, &t.Attempts, &t.NextAt, &t.Result, &t.CallID, &t.Seconds, &t.Summary, &t.Answers, &t.Ref)
}

// listTargets: GET /api/campaigns/{id}/targets?status=&offset=&numbers=1: the
// results table, a row per person, with the form of the person's last call.
func (s *Server) listTargets(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	c, err := s.campaign(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		return err
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, err := s.DB.Query(r.Context(), targetQuery+`and ($2 = '' or t.status = $2) order by t.row_no limit 200 offset $3`, c.ID, q.Get("status"), max(offset, 0))
	if err != nil {
		return err
	}
	targets, err := pgx.CollectRows(rows, scanTarget)
	if err != nil {
		return err
	}
	if q.Get("numbers") != "1" {
		for i := range targets {
			targets[i].Phone = phone.Mask(targets[i].Phone)
		}
	}
	if targets == nil {
		targets = []targetView{}
	}
	_, v, err := s.version(r.Context(), ws, c.VersionID)
	if err != nil {
		return err
	}
	form := v.Spec.Form
	if form == nil {
		form = []model.Field{}
	}
	writeJSON(w, 200, map[string]any{"targets": targets, "form": form})
	return nil
}

// exportCampaign: the results table as a workbook: the list's own columns,
// then where each person stands, then the form.
func (s *Server) exportCampaign(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	c, err := s.campaign(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		return err
	}
	_, v, err := s.version(r.Context(), ws, c.VersionID)
	if err != nil {
		return err
	}
	var columns []sheets.Column
	if c.ListID != nil {
		var raw []byte
		if err := s.DB.QueryRow(r.Context(), `select columns from contact_lists where id = $1`, *c.ListID).Scan(&raw); err == nil {
			_ = json.Unmarshal(raw, &columns)
		}
	}
	headers := []string{"Телефон"}
	for _, col := range columns {
		headers = append(headers, col.Title)
	}
	headers = append(headers, "Статус", "Попыток", "Итог", "Длительность, с", "Кратко")
	for _, f := range v.Spec.Form {
		headers = append(headers, f.Title)
	}
	rows, err := s.DB.Query(r.Context(), targetQuery+`order by t.row_no`, c.ID)
	if err != nil {
		return err
	}
	targets, err := pgx.CollectRows(rows, scanTarget)
	if err != nil {
		return err
	}
	table := make([][]string, 0, len(targets))
	for _, t := range targets {
		vars := map[string]string{}
		_ = json.Unmarshal(t.Vars, &vars)
		answers := map[string]model.Answer{}
		if t.Answers != nil {
			_ = json.Unmarshal(t.Answers, &answers)
		}
		row := []string{t.Phone}
		for _, col := range columns {
			row = append(row, vars[col.Key])
		}
		seconds, summary := "", ""
		if t.Seconds != nil {
			seconds = strconv.Itoa(int(*t.Seconds + 0.5))
		}
		if t.Summary != nil {
			summary = *t.Summary
		}
		row = append(row, t.Status, strconv.Itoa(t.Attempts), t.Result, seconds, summary)
		for _, f := range v.Spec.Form {
			value := ""
			if a, ok := answers[f.Key]; ok && a.Value != nil {
				value = fmt.Sprint(a.Value)
			}
			row = append(row, value)
		}
		table = append(table, row)
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="campaign-`+c.ID[:8]+`.xlsx"`)
	return sheets.Write(w, headers, table)
}
