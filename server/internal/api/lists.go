package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Islomov49/gapi-voice-studio/server/internal/phone"
	"github.com/Islomov49/gapi-voice-studio/server/internal/sheets"
)

type listView struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Columns   json.RawMessage `json:"columns"`
	Stats     json.RawMessage `json:"stats"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Server) listLists(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	rows, err := s.DB.Query(r.Context(), `select id, name, columns, stats, created_at from contact_lists where workspace_id = $1 and archived_at is null order by created_at desc`, ws.ID)
	if err != nil {
		return err
	}
	lists, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (listView, error) {
		l := listView{}
		return l, row.Scan(&l.ID, &l.Name, &l.Columns, &l.Stats, &l.CreatedAt)
	})
	if err != nil {
		return err
	}
	if lists == nil {
		lists = []listView{}
	}
	writeJSON(w, 200, map[string]any{"lists": lists})
	return nil
}

// uploadList: POST /api/lists, multipart: "file" (.xlsx or .csv, the first row
// its headers), "name", and "phone_column" (a header's title) when the column
// with the numbers should not be guessed.
func (s *Server) uploadList(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		return bad("file_missing", "attach an .xlsx or .csv file of at most 10 MB")
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return bad("file_missing", "the file could not be read")
	}
	sheet, err := sheets.Read(header.Filename, data)
	if err != nil {
		return bad("file_invalid", err.Error())
	}
	column := -1
	if want := strings.TrimSpace(r.FormValue("phone_column")); want != "" {
		for i, h := range sheet.Headers {
			if strings.EqualFold(strings.TrimSpace(h), want) {
				column = i
			}
		}
		if column < 0 {
			return bad("phone_column_unknown", "the file has no column \""+want+"\"")
		}
	} else if column = sheet.PhoneColumn(); column < 0 {
		return &fail{Status: http.StatusBadRequest, Code: "phone_column_needed", Hint: "which column holds the phone numbers?", Extra: map[string]any{"headers": sheet.Headers}}
	}
	columns, contacts, stats := sheet.Contacts(column)
	if len(contacts) == 0 {
		return bad("file_invalid", "no row of the file has a phone number in column \""+sheet.Headers[column]+"\"")
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(header.Filename, "."+lastExt(header.Filename))
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	// how many of the accepted numbers are on the stop list: they stay in the
	// list and are skipped when a campaign reaches them
	numbers := make([]string, len(contacts))
	for i, c := range contacts {
		numbers[i] = c.Phone
	}
	var stopped int
	if err := s.DB.QueryRow(r.Context(), `select count(*) from stoplist where workspace_id = $1 and phone = any($2)`, ws.ID, numbers).Scan(&stopped); err != nil {
		return err
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var id string
	statsOut := map[string]any{"rows": stats.Rows, "accepted": stats.Accepted, "duplicates": stats.Duplicates, "invalid": stats.Invalid, "stopped": stopped,
		"phone_column": strings.TrimSpace(sheet.Headers[column])}
	if err := tx.QueryRow(r.Context(), `insert into contact_lists (workspace_id, name, columns, stats) values ($1, $2, $3, $4) returning id`,
		ws.ID, name, columns, statsOut).Scan(&id); err != nil {
		return err
	}
	rows := make([][]any, len(contacts))
	for i, c := range contacts {
		rows[i] = []any{id, c.Row, c.Phone, c.Data}
	}
	if _, err := tx.CopyFrom(r.Context(), pgx.Identifier{"contacts"}, []string{"list_id", "row_no", "phone", "data"}, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name, "columns": columns, "stats": statsOut})
	return nil
}

func lastExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return ""
}

// getList: the list and a page of its contacts. Numbers are shown whole only
// with ?numbers=1: a list on a shared screen need not show them.
func (s *Server) getList(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	l := listView{}
	if err := s.DB.QueryRow(r.Context(), `select id, name, columns, stats, created_at from contact_lists where id = $1 and workspace_id = $2 and archived_at is null`,
		r.PathValue("id"), ws.ID).Scan(&l.ID, &l.Name, &l.Columns, &l.Stats, &l.CreatedAt); err != nil {
		return notFound("list")
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	rows, err := s.DB.Query(r.Context(), `select row_no, phone, data from contacts where list_id = $1 order by row_no limit 100 offset $2`, l.ID, max(offset, 0))
	if err != nil {
		return err
	}
	whole := r.URL.Query().Get("numbers") == "1"
	type contact struct {
		Row   int             `json:"row"`
		Phone string          `json:"phone"`
		Data  json.RawMessage `json:"data"`
	}
	contacts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (contact, error) {
		c := contact{}
		err := row.Scan(&c.Row, &c.Phone, &c.Data)
		if !whole {
			c.Phone = phone.Mask(c.Phone)
		}
		return c, err
	})
	if err != nil {
		return err
	}
	if contacts == nil {
		contacts = []contact{}
	}
	writeJSON(w, 200, map[string]any{"list": l, "contacts": contacts})
	return nil
}

func (s *Server) archiveList(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	tag, err := s.DB.Exec(r.Context(), `update contact_lists set archived_at = $3 where id = $1 and workspace_id = $2 and archived_at is null`, r.PathValue("id"), ws.ID, s.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFound("list")
	}
	writeJSON(w, 200, map[string]any{"archived": true})
	return nil
}

func (s *Server) listStop(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	rows, err := s.DB.Query(r.Context(), `select phone, reason, source, call_id, created_at from stoplist where workspace_id = $1 order by created_at desc, source, phone limit 1000`, ws.ID)
	if err != nil {
		return err
	}
	type entry struct {
		Phone     string    `json:"phone"`
		Reason    string    `json:"reason"`
		Source    string    `json:"source"`
		CallID    *string   `json:"call_id"`
		CreatedAt time.Time `json:"created_at"`
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (entry, error) {
		e := entry{}
		return e, row.Scan(&e.Phone, &e.Reason, &e.Source, &e.CallID, &e.CreatedAt)
	})
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []entry{}
	}
	writeJSON(w, 200, map[string]any{"stoplist": entries})
	return nil
}

// addStop: POST /api/stoplist {"phones": ["..."], "reason"}.
func (s *Server) addStop(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	var req struct {
		Phones []string `json:"phones"`
		Reason string   `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if len(req.Phones) == 0 || len(req.Phones) > 5000 {
		return bad("phones_invalid", "send 1 to 5,000 numbers")
	}
	added, invalid := 0, 0
	for _, p := range req.Phones {
		number, ok := phone.Normalize(p)
		if !ok {
			invalid++
			continue
		}
		tag, err := s.DB.Exec(r.Context(), `insert into stoplist (workspace_id, phone, reason, source, created_at) values ($1, $2, $3, 'manual', $4) on conflict do nothing`,
			ws.ID, number, strings.TrimSpace(req.Reason), s.Now())
		if err != nil {
			return err
		}
		added += int(tag.RowsAffected())
	}
	writeJSON(w, 200, map[string]any{"added": added, "invalid": invalid})
	return nil
}

// removeStop: a person takes a number off the stop list; nothing else does.
func (s *Server) removeStop(w http.ResponseWriter, r *http.Request, ws *Workspace) error {
	tag, err := s.DB.Exec(r.Context(), `delete from stoplist where workspace_id = $1 and phone = $2`, ws.ID, r.PathValue("phone"))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFound("number")
	}
	writeJSON(w, 200, map[string]any{"removed": true})
	return nil
}
