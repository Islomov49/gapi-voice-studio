// Package sheets reads an uploaded list of contacts (.xlsx or .csv) and
// writes a campaign's results as .xlsx.
package sheets

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"github.com/Islomov49/gapi-voice-studio/server/internal/phone"
)

const (
	MaxRows    = 20000
	MaxColumns = 40
)

type Sheet struct {
	Headers []string
	Rows    [][]string
}

// Read takes the first sheet of a workbook, or a CSV with "," or ";" between
// values; the first row is the headers.
func Read(name string, data []byte) (*Sheet, error) {
	var rows [][]string
	switch strings.ToLower(filepath.Ext(name)) {
	case ".xlsx", ".xlsm":
		f, err := excelize.OpenReader(bytes.NewReader(data))
		if err != nil {
			return nil, errors.New("the file is not a readable .xlsx workbook")
		}
		defer f.Close()
		if rows, err = f.GetRows(f.GetSheetName(0)); err != nil {
			return nil, errors.New("the workbook's first sheet could not be read")
		}
	case ".csv", ".txt":
		if !utf8.Valid(data) {
			return nil, errors.New("the .csv is not UTF-8: save it as \"CSV UTF-8\" or upload the .xlsx")
		}
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		r := csv.NewReader(bytes.NewReader(data))
		first, _, _ := bytes.Cut(data, []byte("\n"))
		if bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
			r.Comma = ';'
		}
		r.FieldsPerRecord, r.LazyQuotes = -1, true
		var err error
		if rows, err = r.ReadAll(); err != nil {
			return nil, errors.New("the .csv could not be read: " + err.Error())
		}
	default:
		return nil, errors.New("upload an .xlsx or .csv file")
	}
	for len(rows) > 0 && blank(rows[0]) {
		rows = rows[1:]
	}
	if len(rows) < 2 {
		return nil, errors.New("the file needs a row of headers and at least one row under it")
	}
	s := &Sheet{Headers: rows[0]}
	if len(s.Headers) > MaxColumns {
		return nil, fmt.Errorf("the file has %d columns, at most %d", len(s.Headers), MaxColumns)
	}
	for _, row := range rows[1:] {
		if !blank(row) {
			s.Rows = append(s.Rows, row)
		}
	}
	if len(s.Rows) > MaxRows {
		return nil, fmt.Errorf("the file has %d rows, at most %d in one list", len(s.Rows), MaxRows)
	}
	return s, nil
}

func blank(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

func cell(row []string, i int) string {
	if i < len(row) {
		return strings.TrimSpace(row[i])
	}
	return ""
}

// PhoneColumn is the column most of whose values read as phone numbers, or -1.
func (s *Sheet) PhoneColumn() int {
	best, bestHits := -1, 0
	for c := range s.Headers {
		hits := 0
		for _, row := range s.Rows {
			if _, ok := phone.Normalize(cell(row, c)); ok {
				hits++
			}
		}
		if hits > bestHits {
			best, bestHits = c, hits
		}
	}
	if bestHits*2 < len(s.Rows) {
		return -1
	}
	return best
}

// Column is a column of the sheet besides the phone: the key a script names
// it by ({{key}}) and its title as uploaded.
type Column struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	index int
}

// Contact is one accepted row.
type Contact struct {
	Row   int
	Phone string
	Data  map[string]string
	// The card in a CRM the person stands for, if any.
	Ref string
}

// Column makes the column of a key and a title, for lists that do not come from a sheet.
func NewColumn(key, title string) Column { return Column{Key: key, Title: title} }

// Stats is what an upload found.
type Stats struct {
	Rows       int `json:"rows"`
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Invalid    int `json:"invalid"`
}

// Contacts reads the rows as contacts: the phone from phoneColumn, every other
// column with a header as data. A number met twice is kept once.
func (s *Sheet) Contacts(phoneColumn int) ([]Column, []Contact, Stats) {
	columns := []Column{}
	taken := map[string]bool{}
	for i, h := range s.Headers {
		if i == phoneColumn || strings.TrimSpace(h) == "" {
			continue
		}
		key := Key(h)
		for n := 2; taken[key]; n++ {
			key = fmt.Sprintf("%.36s_%d", Key(h), n)
		}
		taken[key] = true
		columns = append(columns, Column{Key: key, Title: strings.TrimSpace(h), index: i})
	}
	stats := Stats{Rows: len(s.Rows)}
	seen := map[string]bool{}
	var contacts []Contact
	for i, row := range s.Rows {
		number, ok := phone.Normalize(cell(row, phoneColumn))
		switch {
		case !ok:
			stats.Invalid++
			continue
		case seen[number]:
			stats.Duplicates++
			continue
		}
		seen[number] = true
		data := map[string]string{}
		for _, c := range columns {
			if v := cell(row, c.index); v != "" {
				data[c.Key] = v
			}
		}
		contacts = append(contacts, Contact{Row: i + 2, Phone: number, Data: data})
	}
	stats.Accepted = len(contacts)
	return columns, contacts, stats
}

var names = map[string]bool{"имя": true, "фио": true, "ф.и.о": true, "ф.и.о.": true, "клиент": true, "ism": true, "ismi": true, "mijoz": true, "name": true, "fio": true, "f.i.o": true, "full name": true, "client": true}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "j", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "x", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sh", 'ъ': "",
	'ы': "i", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya", 'ў': "o", 'қ': "q", 'ғ': "g", 'ҳ': "h",
}

// Key makes a variable's name of a column's header: "Имя клиента" is
// "imya_klienta"; a header that means the person's name is "client_name",
// which is what the Studio's templates call it.
func Key(header string) string {
	h := strings.ToLower(strings.TrimSpace(header))
	if names[h] {
		return "client_name"
	}
	var b strings.Builder
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case translit[r] != "":
			b.WriteString(translit[r])
		case r == 'ъ' || r == 'ь' || r == '\'' || r == 'ʻ' || r == '’':
		default:
			b.WriteByte('_')
		}
	}
	key := strings.Trim(b.String(), "_")
	for strings.Contains(key, "__") {
		key = strings.ReplaceAll(key, "__", "_")
	}
	switch {
	case key == "":
		key = "column"
	case key[0] >= '0' && key[0] <= '9':
		key = "c_" + key
	}
	if len(key) > 40 {
		key = strings.TrimRight(key[:40], "_")
	}
	return key
}

// Write makes a one-sheet workbook of the table.
func Write(w io.Writer, headers []string, rows [][]string) error {
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0)
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	line := func(n int, values []string) error {
		out := make([]any, len(values))
		for i, v := range values {
			out[i] = v
		}
		at, _ := excelize.CoordinatesToCellName(1, n)
		return sw.SetRow(at, out)
	}
	if err := line(1, headers); err != nil {
		return err
	}
	for i, row := range rows {
		if err := line(i+2, row); err != nil {
			return err
		}
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	return f.Write(w)
}
