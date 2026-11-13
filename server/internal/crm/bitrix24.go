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

// Bitrix24 through an inbound webhook: the portal's administrator makes one
// (Developer resources, Inbound webhook) with the right "CRM" and gives its
// address, https://<portal>/rest/<user>/<secret>/. Every method is
// a POST to that address plus the method's name.
type Bitrix24 struct {
	// https://<portal>/rest/<user>/<secret>/
	Webhook string
	HTTP    *http.Client
}

// SplitBitrixWebhook takes the address as pasted and answers the portal's
// address, to show, and the path with the secret, to keep sealed.
func SplitBitrixWebhook(pasted string) (base, secret string, err error) {
	u, perr := url.Parse(strings.TrimSpace(pasted))
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return "", "", errors.New("paste the inbound webhook's address: https://<portal>/rest/<user>/<secret>/")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "rest" || parts[1] == "" || parts[2] == "" {
		return "", "", errors.New("the address should look like https://<portal>/rest/<user>/<secret>/")
	}
	return "https://" + u.Host, "/rest/" + parts[1] + "/" + parts[2] + "/", nil
}

func NewBitrix24(base, secret string) *Bitrix24 {
	return &Bitrix24{Webhook: base + secret, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type bitrixAnswer struct {
	Result json.RawMessage `json:"result"`
	Next   *int            `json:"next"`
	Error  string          `json:"error"`
	Hint   string          `json:"error_description"`
}

func (b *Bitrix24) call(ctx context.Context, method string, params any, out any) (*int, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.Webhook+method+".json", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := b.HTTP.Do(req)
		if err != nil {
			return nil, errors.New("Bitrix24 did not answer")
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		var a bitrixAnswer
		notJSON := json.Unmarshal(data, &a) != nil
		// the portal allows two requests a second: wait and ask again
		busy := strings.EqualFold(a.Error, "QUERY_LIMIT_EXCEEDED") || resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout
		if busy && attempt < 4 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 700 * time.Millisecond):
			}
			continue
		}
		if notJSON {
			return nil, fmt.Errorf("Bitrix24 answered %d with something that is not JSON", resp.StatusCode)
		}
		if a.Error != "" {
			return nil, fmt.Errorf("Bitrix24 %s: %s", a.Error, a.Hint)
		}
		if out != nil {
			if err := json.Unmarshal(a.Result, out); err != nil {
				return nil, fmt.Errorf("Bitrix24 %s: an answer of another shape", method)
			}
		}
		return a.Next, nil
	}
}

func (b *Bitrix24) Account(ctx context.Context) (string, error) {
	var statuses []json.RawMessage
	if _, err := b.call(ctx, "crm.status.list", map[string]any{"filter": map[string]any{"ENTITY_ID": "STATUS"}}, &statuses); err != nil {
		return "", err
	}
	u, _ := url.Parse(b.Webhook)
	return u.Host, nil
}

type bitrixStatus struct {
	StatusID string `json:"STATUS_ID"`
	Name     string `json:"NAME"`
}

func (b *Bitrix24) stages(ctx context.Context, entityID string) ([]Stage, error) {
	var statuses []bitrixStatus
	if _, err := b.call(ctx, "crm.status.list", map[string]any{"filter": map[string]any{"ENTITY_ID": entityID}, "order": map[string]any{"SORT": "ASC"}}, &statuses); err != nil {
		return nil, err
	}
	stages := make([]Stage, 0, len(statuses))
	for _, s := range statuses {
		stages = append(stages, Stage{ID: s.StatusID, Name: s.Name})
	}
	return stages, nil
}

func (b *Bitrix24) Pipelines(ctx context.Context) ([]Pipeline, error) {
	var out []Pipeline
	leads, err := b.stages(ctx, "STATUS")
	if err != nil {
		return nil, err
	}
	out = append(out, Pipeline{Entity: "lead", ID: "", Name: "Лиды", Stages: leads})
	var cats struct {
		Categories []struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
		} `json:"categories"`
	}
	if _, err := b.call(ctx, "crm.category.list", map[string]any{"entityTypeId": 2}, &cats); err != nil {
		return nil, err
	}
	for _, c := range cats.Categories {
		id := c.ID.String()
		entity := "DEAL_STAGE_" + id
		if id == "0" {
			entity = "DEAL_STAGE"
		}
		stages, err := b.stages(ctx, entity)
		if err != nil {
			return nil, err
		}
		out = append(out, Pipeline{Entity: "deal", ID: id, Name: c.Name, Stages: stages})
	}
	return out, nil
}

type bitrixPhone struct {
	Value string `json:"VALUE"`
}

type bitrixCard struct {
	ID        string        `json:"ID"`
	Title     string        `json:"TITLE"`
	Name      string        `json:"NAME"`
	LastName  string        `json:"LAST_NAME"`
	Phone     []bitrixPhone `json:"PHONE"`
	ContactID string        `json:"CONTACT_ID"`
	Assigned  string        `json:"ASSIGNED_BY_ID"`
	Amount    string        `json:"OPPORTUNITY"`
}

func first(phones []bitrixPhone) string {
	for _, p := range phones {
		if strings.TrimSpace(p.Value) != "" {
			return p.Value
		}
	}
	return ""
}

func (b *Bitrix24) Cards(ctx context.Context, entity, pipelineID, stageID string, limit int) ([]Card, error) {
	method, filter, sel := "crm.lead.list", map[string]any{"STATUS_ID": stageID}, []string{"ID", "TITLE", "NAME", "LAST_NAME", "PHONE", "ASSIGNED_BY_ID", "OPPORTUNITY"}
	if entity == "deal" {
		method, filter, sel = "crm.deal.list", map[string]any{"CATEGORY_ID": pipelineID, "STAGE_ID": stageID}, []string{"ID", "TITLE", "CONTACT_ID", "ASSIGNED_BY_ID", "OPPORTUNITY"}
	}
	var rows []bitrixCard
	start := 0
	for len(rows) < limit {
		var page []bitrixCard
		next, err := b.call(ctx, method, map[string]any{"filter": filter, "select": sel, "order": map[string]any{"ID": "DESC"}, "start": start}, &page)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page...)
		if next == nil || len(page) == 0 {
			break
		}
		start = *next
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	// a deal's person is its contact: read the contacts in pages of fifty
	contacts := map[string]bitrixCard{}
	if entity == "deal" {
		var ids []string
		for _, r := range rows {
			if r.ContactID != "" && r.ContactID != "0" {
				ids = append(ids, r.ContactID)
			}
		}
		for len(ids) > 0 {
			n := min(len(ids), 50)
			var page []bitrixCard
			if _, err := b.call(ctx, "crm.contact.list", map[string]any{"filter": map[string]any{"@ID": ids[:n]}, "select": []string{"ID", "NAME", "LAST_NAME", "PHONE"}}, &page); err != nil {
				return nil, err
			}
			for _, c := range page {
				contacts[c.ID] = c
			}
			ids = ids[n:]
		}
	}
	cards := make([]Card, 0, len(rows))
	for _, r := range rows {
		person := r
		if entity == "deal" {
			person = contacts[r.ContactID]
		}
		c := Card{Entity: entity, ID: r.ID, Title: r.Title, Name: strings.TrimSpace(person.Name + " " + person.LastName), Phone: first(person.Phone), Responsible: r.Assigned, Fields: map[string]string{}}
		if amount, err := strconv.ParseFloat(r.Amount, 64); err == nil && amount > 0 {
			c.Fields["amount"] = strconv.FormatFloat(amount, 'f', -1, 64)
		}
		cards = append(cards, c)
	}
	return cards, nil
}

func (b *Bitrix24) Note(ctx context.Context, entity, id, text string) error {
	_, err := b.call(ctx, "crm.timeline.comment.add", map[string]any{"fields": map[string]any{"ENTITY_ID": id, "ENTITY_TYPE": entity, "COMMENT": text}}, nil)
	return err
}

func (b *Bitrix24) Move(ctx context.Context, entity, id, _, stageID string) error {
	method, field := "crm.lead.update", "STATUS_ID"
	if entity == "deal" {
		method, field = "crm.deal.update", "STAGE_ID"
	}
	_, err := b.call(ctx, method, map[string]any{"id": id, "fields": map[string]any{field: stageID}}, nil)
	return err
}

// Task is a to-do in the card itself (crm.activity.todo.add): it shows in the
// card's timeline for the person responsible and needs no right beyond "CRM".
func (b *Bitrix24) Task(ctx context.Context, entity, id, responsible, text string, due time.Time) error {
	owner := 1 // a lead
	if entity == "deal" {
		owner = 2
	}
	title := text
	if r := []rune(title); len(r) > 120 {
		title = string(r[:120])
	}
	params := map[string]any{"ownerTypeId": owner, "ownerId": id, "title": title, "description": text, "deadline": due.UTC().Format(time.RFC3339)}
	if responsible != "" {
		params["responsibleId"] = responsible
	}
	_, err := b.call(ctx, "crm.activity.todo.add", params, nil)
	return err
}

func (b *Bitrix24) Responsible(ctx context.Context, entity, id string) (string, error) {
	method := "crm.lead.get"
	if entity == "deal" {
		method = "crm.deal.get"
	}
	var card bitrixCard
	_, err := b.call(ctx, method, map[string]any{"id": id}, &card)
	return card.Assigned, err
}
