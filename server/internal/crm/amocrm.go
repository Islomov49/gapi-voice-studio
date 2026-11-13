package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AmoCRM (Kommo) through its API v4 with a long-lived token: the account's
// administrator makes a private integration and copies its long-lived token.
// What amoCRM calls a lead is the card that moves through a pipeline.
type AmoCRM struct {
	// https://<account>.amocrm.ru
	Base  string
	Token string
	HTTP  *http.Client
}

// AmoBase reads the account's address as pasted: "mycompany",
// "mycompany.amocrm.ru" or a whole link.
func AmoBase(pasted string) (string, error) {
	s := strings.TrimSpace(pasted)
	if s == "" {
		return "", errors.New("give the account's address, like mycompany.amocrm.ru")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", errors.New("give the account's address, like mycompany.amocrm.ru")
	}
	host := u.Host
	if !strings.Contains(host, ".") {
		host += ".amocrm.ru"
	}
	return "https://" + host, nil
}

func NewAmoCRM(base, token string) *AmoCRM {
	return &AmoCRM{Base: base, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (a *AmoCRM) call(ctx context.Context, method, path string, body, out any) error {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, a.Base+path, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+a.Token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := a.HTTP.Do(req)
		if err != nil {
			return errors.New("amoCRM did not answer")
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests && attempt < 4:
			// seven requests a second at most: wait and ask again
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 700 * time.Millisecond):
			}
			continue
		case resp.StatusCode == http.StatusNoContent:
			return nil // an empty list
		case resp.StatusCode == http.StatusUnauthorized:
			return errors.New("amoCRM did not accept the token")
		case resp.StatusCode >= 300:
			var e struct {
				Title  string `json:"title"`
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(data, &e)
			return fmt.Errorf("amoCRM %d: %s %s", resp.StatusCode, e.Title, e.Detail)
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("amoCRM %s: an answer of another shape", path)
		}
		return nil
	}
}

func (a *AmoCRM) Account(ctx context.Context) (string, error) {
	var acc struct {
		Name string `json:"name"`
	}
	if err := a.call(ctx, http.MethodGet, "/api/v4/account", nil, &acc); err != nil {
		return "", err
	}
	return acc.Name, nil
}

func (a *AmoCRM) Pipelines(ctx context.Context) ([]Pipeline, error) {
	var resp struct {
		Embedded struct {
			Pipelines []struct {
				ID       int64  `json:"id"`
				Name     string `json:"name"`
				Embedded struct {
					Statuses []struct {
						ID   int64  `json:"id"`
						Name string `json:"name"`
					} `json:"statuses"`
				} `json:"_embedded"`
			} `json:"pipelines"`
		} `json:"_embedded"`
	}
	if err := a.call(ctx, http.MethodGet, "/api/v4/leads/pipelines", nil, &resp); err != nil {
		return nil, err
	}
	var out []Pipeline
	for _, p := range resp.Embedded.Pipelines {
		pl := Pipeline{Entity: "lead", ID: strconv.FormatInt(p.ID, 10), Name: p.Name}
		for _, s := range p.Embedded.Statuses {
			pl.Stages = append(pl.Stages, Stage{ID: strconv.FormatInt(s.ID, 10), Name: s.Name})
		}
		out = append(out, pl)
	}
	return out, nil
}

type amoLead struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Price       int64  `json:"price"`
	Responsible int64  `json:"responsible_user_id"`
	Embedded    struct {
		Contacts []struct {
			ID     int64 `json:"id"`
			IsMain bool  `json:"is_main"`
		} `json:"contacts"`
	} `json:"_embedded"`
}

type amoContact struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Fields []struct {
		Code   string `json:"field_code"`
		Values []struct {
			Value any `json:"value"`
		} `json:"values"`
	} `json:"custom_fields_values"`
}

func (c amoContact) phone() string {
	for _, f := range c.Fields {
		if f.Code == "PHONE" {
			for _, v := range f.Values {
				if s := strings.TrimSpace(fmt.Sprint(v.Value)); s != "" && v.Value != nil {
					return s
				}
			}
		}
	}
	return ""
}

func (a *AmoCRM) Cards(ctx context.Context, _, pipelineID, stageID string, limit int) ([]Card, error) {
	var leads []amoLead
	for page := 1; len(leads) < limit; page++ {
		q := url.Values{"with": {"contacts"}, "limit": {"250"}, "page": {strconv.Itoa(page)}, "order[id]": {"desc"},
			"filter[statuses][0][pipeline_id]": {pipelineID}, "filter[statuses][0][status_id]": {stageID}}
		var resp struct {
			Embedded struct {
				Leads []amoLead `json:"leads"`
			} `json:"_embedded"`
		}
		if err := a.call(ctx, http.MethodGet, "/api/v4/leads?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		leads = append(leads, resp.Embedded.Leads...)
		if len(resp.Embedded.Leads) < 250 {
			break
		}
	}
	if len(leads) > limit {
		leads = leads[:limit]
	}
	// a lead's person is its main contact
	main := map[int64]int64{}
	var ids []int64
	for _, l := range leads {
		for i, c := range l.Embedded.Contacts {
			if c.IsMain || i == 0 {
				main[l.ID] = c.ID
			}
		}
		if id, ok := main[l.ID]; ok {
			ids = append(ids, id)
		}
	}
	contacts := map[int64]amoContact{}
	for len(ids) > 0 {
		n := min(len(ids), 50)
		q := url.Values{"limit": {"250"}}
		for _, id := range ids[:n] {
			q.Add("filter[id][]", strconv.FormatInt(id, 10))
		}
		var resp struct {
			Embedded struct {
				Contacts []amoContact `json:"contacts"`
			} `json:"_embedded"`
		}
		if err := a.call(ctx, http.MethodGet, "/api/v4/contacts?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		for _, c := range resp.Embedded.Contacts {
			contacts[c.ID] = c
		}
		ids = ids[n:]
	}
	cards := make([]Card, 0, len(leads))
	for _, l := range leads {
		person := contacts[main[l.ID]]
		c := Card{Entity: "lead", ID: strconv.FormatInt(l.ID, 10), Title: l.Name, Name: person.Name, Phone: person.phone(), Fields: map[string]string{}}
		if l.Responsible != 0 {
			c.Responsible = strconv.FormatInt(l.Responsible, 10)
		}
		if l.Price > 0 {
			c.Fields["amount"] = strconv.FormatInt(l.Price, 10)
		}
		cards = append(cards, c)
	}
	return cards, nil
}

func (a *AmoCRM) Note(ctx context.Context, _, id, text string) error {
	return a.call(ctx, http.MethodPost, "/api/v4/leads/"+url.PathEscape(id)+"/notes", []map[string]any{{"note_type": "common", "params": map[string]any{"text": text}}}, nil)
}

func (a *AmoCRM) Move(ctx context.Context, _, id, pipelineID, stageID string) error {
	lead, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return errors.New("amoCRM: a lead's id is a number")
	}
	status, err := strconv.ParseInt(stageID, 10, 64)
	if err != nil {
		return errors.New("amoCRM: a stage's id is a number")
	}
	change := map[string]any{"id": lead, "status_id": status}
	if n, err := strconv.ParseInt(pipelineID, 10, 64); err == nil {
		change["pipeline_id"] = n
	}
	return a.call(ctx, http.MethodPatch, "/api/v4/leads", []map[string]any{change}, nil)
}

func (a *AmoCRM) Task(ctx context.Context, _, id, responsible, text string, due time.Time) error {
	entity, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return errors.New("amoCRM: a lead's id is a number")
	}
	task := map[string]any{"text": text, "complete_till": due.Unix(), "entity_id": entity, "entity_type": "leads", "task_type_id": 1}
	if n, err := strconv.ParseInt(responsible, 10, 64); err == nil && n != 0 {
		task["responsible_user_id"] = n
	}
	return a.call(ctx, http.MethodPost, "/api/v4/tasks", []map[string]any{task}, nil)
}

func (a *AmoCRM) Responsible(ctx context.Context, _, id string) (string, error) {
	var lead amoLead
	if err := a.call(ctx, http.MethodGet, "/api/v4/leads/"+url.PathEscape(id), nil, &lead); err != nil || lead.Responsible == 0 {
		return "", err
	}
	return strconv.FormatInt(lead.Responsible, 10), nil
}
