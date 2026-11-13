// Package crmtest stands in for a Bitrix24 portal and an amoCRM account in
// the Studio's tests: the few methods the connectors use, answering in the
// vendors' shapes, and a record of what was written.
package crmtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type Deal struct {
	ID, Title, Stage, Contact, Assigned string
}

// Bitrix is a portal with one extra deal category (id 3) beside the default.
type Bitrix struct {
	*httptest.Server
	mu       sync.Mutex
	Deals    []Deal
	Contacts map[string][2]string // id: name, phone
	// What was written: "comment deal 7: text", "move deal 7 WON", "todo deal 7 for 5: text".
	Written []string
	// Answer the next request as a portal over its limit does.
	Busy bool
}

// Webhook is the address a portal's administrator would paste. The test
// server speaks plain http, so the connector is made with Base and Secret.
func (b *Bitrix) Secret() string { return "/rest/1/secretsecret/" }

func NewBitrix() *Bitrix {
	b := &Bitrix{Contacts: map[string][2]string{}}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, b.Secret()), ".json")
		raw, _ := io.ReadAll(r.Body)
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		b.mu.Lock()
		defer b.mu.Unlock()
		answer := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"result": result}) }
		if !strings.HasPrefix(r.URL.Path, b.Secret()) {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "INVALID_CREDENTIALS", "error_description": "wrong webhook"})
			return
		}
		if b.Busy {
			b.Busy = false
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "QUERY_LIMIT_EXCEEDED", "error_description": "Too many requests"})
			return
		}
		filter, _ := p["filter"].(map[string]any)
		fields, _ := p["fields"].(map[string]any)
		switch method {
		case "crm.status.list":
			switch filter["ENTITY_ID"] {
			case "STATUS":
				answer([]map[string]any{{"STATUS_ID": "NEW", "NAME": "Новый"}, {"STATUS_ID": "IN_PROCESS", "NAME": "В работе"}})
			case "DEAL_STAGE":
				answer([]map[string]any{{"STATUS_ID": "NEW", "NAME": "Новая"}, {"STATUS_ID": "WON", "NAME": "Успех"}, {"STATUS_ID": "LOSE", "NAME": "Провал"}})
			default:
				answer([]map[string]any{{"STATUS_ID": "C3:NEW", "NAME": "Заявка"}})
			}
		case "crm.category.list":
			answer(map[string]any{"categories": []map[string]any{{"id": 0, "name": "Общая"}, {"id": 3, "name": "Опт"}}})
		case "crm.deal.list":
			var out []map[string]any
			for _, d := range b.Deals {
				if d.Stage == filter["STAGE_ID"] {
					out = append(out, map[string]any{"ID": d.ID, "TITLE": d.Title, "CONTACT_ID": d.Contact, "ASSIGNED_BY_ID": d.Assigned, "OPPORTUNITY": "150000.00"})
				}
			}
			answer(out)
		case "crm.contact.list":
			var out []map[string]any
			for _, id := range filter["@ID"].([]any) {
				if c, ok := b.Contacts[id.(string)]; ok {
					out = append(out, map[string]any{"ID": id, "NAME": c[0], "LAST_NAME": "", "PHONE": []map[string]any{{"VALUE": c[1], "VALUE_TYPE": "MOBILE"}}})
				}
			}
			answer(out)
		case "crm.deal.get":
			for _, d := range b.Deals {
				if d.ID == fmt.Sprint(p["id"]) {
					answer(map[string]any{"ID": d.ID, "ASSIGNED_BY_ID": d.Assigned})
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "NOT_FOUND", "error_description": "no deal"})
		case "crm.timeline.comment.add":
			b.Written = append(b.Written, fmt.Sprintf("comment %v %v: %v", fields["ENTITY_TYPE"], fields["ENTITY_ID"], fields["COMMENT"]))
			answer(1)
		case "crm.deal.update":
			b.Written = append(b.Written, fmt.Sprintf("move deal %v %v", p["id"], fields["STAGE_ID"]))
			for i := range b.Deals {
				if b.Deals[i].ID == fmt.Sprint(p["id"]) {
					b.Deals[i].Stage = fmt.Sprint(fields["STAGE_ID"])
				}
			}
			answer(true)
		case "crm.activity.todo.add":
			b.Written = append(b.Written, fmt.Sprintf("todo %v %v for %v: %v", p["ownerTypeId"], p["ownerId"], p["responsibleId"], p["title"]))
			answer(map[string]any{"id": 9})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "ERROR_METHOD_NOT_FOUND", "error_description": method})
		}
	}))
	return b
}

// Log is what was written to the portal so far.
func (b *Bitrix) Log() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.Written...)
}

func (b *Bitrix) Add(d Deal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Deals = append(b.Deals, d)
}

// Amo is an account with one pipeline (id 10) of two statuses (100, 142).
type Amo struct {
	*httptest.Server
	mu      sync.Mutex
	Written []string
	// lead id: status
	Leads map[int64]int64
}

const AmoToken = "long-lived-token-long-lived-token"

func NewAmo() *Amo {
	a := &Amo{Leads: map[int64]int64{501: 100, 502: 100, 503: 142}}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+AmoToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/v4/account", guard(func(w http.ResponseWriter, r *http.Request) { write(w, map[string]any{"id": 1, "name": "Namuna"}) }))
	mux.HandleFunc("GET /api/v4/leads/pipelines", guard(func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"_embedded": map[string]any{"pipelines": []map[string]any{{"id": 10, "name": "Воронка",
			"_embedded": map[string]any{"statuses": []map[string]any{{"id": 100, "name": "Первичный контакт"}, {"id": 142, "name": "Успешно"}}}}}}})
	}))
	mux.HandleFunc("GET /api/v4/leads", guard(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var leads []map[string]any
		for id, status := range a.Leads {
			if fmt.Sprint(status) == q.Get("filter[statuses][0][status_id]") && q.Get("filter[statuses][0][pipeline_id]") == "10" {
				leads = append(leads, map[string]any{"id": id, "name": fmt.Sprintf("Сделка %d", id), "price": 90000, "responsible_user_id": 77,
					"_embedded": map[string]any{"contacts": []map[string]any{{"id": id + 1000, "is_main": true}}}})
			}
		}
		if len(leads) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		write(w, map[string]any{"_embedded": map[string]any{"leads": leads}})
	}))
	mux.HandleFunc("GET /api/v4/leads/{id}", guard(func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"id": 501, "responsible_user_id": 77})
	}))
	mux.HandleFunc("GET /api/v4/contacts", guard(func(w http.ResponseWriter, r *http.Request) {
		var contacts []map[string]any
		for _, id := range r.URL.Query()["filter[id][]"] {
			contacts = append(contacts, map[string]any{"id": json.Number(id), "name": "Mijoz " + id,
				"custom_fields_values": []map[string]any{{"field_code": "EMAIL", "values": []map[string]any{{"value": "a@b.c"}}},
					{"field_code": "PHONE", "values": []map[string]any{{"value": "+99890222" + id[len(id)-4:]}}}}})
		}
		write(w, map[string]any{"_embedded": map[string]any{"contacts": contacts}})
	}))
	body := func(r *http.Request) []map[string]any {
		var out []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&out)
		return out
	}
	mux.HandleFunc("POST /api/v4/leads/{id}/notes", guard(func(w http.ResponseWriter, r *http.Request) {
		for _, n := range body(r) {
			a.Written = append(a.Written, fmt.Sprintf("note %s %v: %v", r.PathValue("id"), n["note_type"], n["params"].(map[string]any)["text"]))
		}
		write(w, map[string]any{})
	}))
	mux.HandleFunc("PATCH /api/v4/leads", guard(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range body(r) {
			a.Written = append(a.Written, fmt.Sprintf("move %v to %v/%v", c["id"], c["pipeline_id"], c["status_id"]))
		}
		write(w, map[string]any{})
	}))
	mux.HandleFunc("POST /api/v4/tasks", guard(func(w http.ResponseWriter, r *http.Request) {
		for _, t := range body(r) {
			a.Written = append(a.Written, fmt.Sprintf("task %v %v for %v: %v", t["entity_type"], t["entity_id"], t["responsible_user_id"], t["text"]))
		}
		write(w, map[string]any{})
	}))
	a.Server = httptest.NewServer(mux)
	return a
}

func (a *Amo) Log() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.Written...)
}
