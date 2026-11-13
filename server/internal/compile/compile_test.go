package compile

import (
	"strings"
	"testing"

	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
	"github.com/Islomov49/gapi-voice-studio/server/internal/phone"
	"github.com/Islomov49/gapi-voice-studio/server/internal/sheets"
	"github.com/Islomov49/gapi-voice-studio/server/internal/templates"
)

func TestTemplatesBuild(t *testing.T) {
	for _, tpl := range templates.All {
		agent, err := Build(tpl.Title, tpl.Spec, DefaultLimits)
		if err != nil {
			t.Fatalf("%s: %v", tpl.ID, err)
		}
		greeting := Greeting(tpl.Spec.Company)
		if strings.Count(agent.Prompt, greeting) != 1 || strings.Contains(agent.Prompt, GreetingPlace) {
			t.Errorf("%s: the greeting should stand once, in the script's own first stage", tpl.ID)
		}
		if !strings.Contains(agent.Prompt, "boshqa qo'ng'iroq qilmang") {
			t.Errorf("%s: the do-not-call rule is missing", tpl.ID)
		}
		if _, ok := agent.Variables["client_name"]; !ok || agent.FirstSpeaker != "caller" {
			t.Errorf("%s: variables %v, first speaker %q", tpl.ID, agent.Variables, agent.FirstSpeaker)
		}
		if _, declared := agent.Variables["studio_greeting"]; declared {
			t.Errorf("%s: the greeting's place is not a variable of the call", tpl.ID)
		}
	}
}

func TestOwnScript(t *testing.T) {
	spec := model.Spec{Company: "Olma", Script: "Siz do'kon yordamchisisiz. Mijoz: {{client_name}}, buyurtma: {{order}}. Bugun {{current_date}}.",
		Form: []model.Field{{Key: "rating", Title: "Оценка", Type: "rating", Ask: "Xizmatni birdan beshgacha baholang."}, {Key: "note", Title: "Заметка", Type: "text"}}}
	agent, err := Build("a", spec, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	// a script without the greeting's place gets the greeting as a rule
	if !strings.Contains(agent.Prompt, "birinchi gapingiz aynan shunday bo'lsin: «"+Greeting("Olma")+"»") {
		t.Error("the greeting rule is missing")
	}
	if !strings.Contains(agent.Prompt, "  - Xizmatni birdan beshgacha baholang.") {
		t.Error("the form's question is missing")
	}
	if len(agent.Variables) != 2 || agent.MaxDurationSeconds != 300 || agent.Voice != "charos" {
		t.Errorf("variables %v, seconds %d, voice %s", agent.Variables, agent.MaxDurationSeconds, agent.Voice)
	}
	values, err := Values(spec, map[string]string{"client_name": " Ali \n Valiyev ", "order": "{{x}}", "unused": "z"})
	if err != nil || values["client_name"] != "Ali Valiyev" || values["order"] != "x" || len(values) != 2 {
		t.Errorf("values %v %v", values, err)
	}
	if p := Preview(spec, map[string]string{"client_name": "Ali"}); !strings.Contains(p, "Mijoz: Ali, buyurtma: .") || !strings.Contains(p, "{{current_date}}") {
		t.Errorf("preview: %s", p)
	}
}

func TestCheck(t *testing.T) {
	good := templates.All[0].Spec
	for name, change := range map[string]func(*model.Spec){
		"no company":          func(s *model.Spec) { s.Company = " " },
		"no script":           func(s *model.Spec) { s.Script = "" },
		"an unknown voice":    func(s *model.Spec) { s.Voice = "zebo" },
		"a bad variable":      func(s *model.Spec) { s.Script += " {{Client Name}} {{1x}}" },
		"a platform variable": func(s *model.Spec) { s.Variables = map[string]string{"current_date": "x"} },
		"a taken form key":    func(s *model.Spec) { s.Form = append(s.Form, model.Field{Key: "outcome", Title: "x", Type: "text"}) },
		"a choice of one": func(s *model.Spec) {
			s.Form = []model.Field{{Key: "a", Title: "x", Type: "choice", Options: []string{"one"}}}
		},
		"too long":        func(s *model.Spec) { s.Script += strings.Repeat(" so'z", 2000) },
		"a call too long": func(s *model.Spec) { s.MaxCallSeconds = 4000 },
	} {
		s := good
		s.Form = append([]model.Field(nil), good.Form...)
		change(&s)
		if len(Check(s, DefaultLimits)) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	if p := Check(good, DefaultLimits); len(p) != 0 {
		t.Errorf("the template itself: %v", p)
	}
}

func TestDisclosed(t *testing.T) {
	turn := func(role, text string) gapi.Turn { return gapi.Turn{Role: role, Text: text} }
	if !Disclosed([]gapi.Turn{turn("agent", "Alo?"), turn("caller", "Alo"), turn("agent", "Assalomu alaykum! Men «Olma» kompaniyasining sunʼiy intellekt yordamchisiman.")}) {
		t.Error("the greeting with another apostrophe should count")
	}
	if Disclosed([]gapi.Turn{turn("agent", "Alo?"), turn("agent", "Assalomu alaykum, men Malikaman."), turn("agent", "Bir daqiqa vaqtingiz bormi?"), turn("agent", "Men sun'iy intellekt yordamchisiman.")}) {
		t.Error("said only in the fourth line is too late")
	}
	if Disclosed([]gapi.Turn{turn("caller", "siz sun'iy intellektmisiz?"), turn("agent", "Yo'q.")}) {
		t.Error("the person's own words do not count")
	}
}

func TestPhonesAndKeys(t *testing.T) {
	for in, want := range map[string]string{
		"+998 (90) 123-45-67": "+998901234567", "998901234567": "+998901234567", "90 123 45 67": "+998901234567", "8 90 1234567": "+998901234567",
		"998901234567.0": "+998901234567", "+7 912 345 67 89": "+79123456789",
		"12345": "", "ali": "", "": "", "90 123 45 6x": "",
	} {
		got, ok := phone.Normalize(in)
		if got != want || ok != (want != "") {
			t.Errorf("Normalize(%q) = %q %v", in, got, ok)
		}
	}
	for in, want := range map[string]string{"Имя": "client_name", "Сумма заказа, сум": "summa_zakaza_sum", "Buyurtma №": "buyurtma", "2-й телефон": "c_2_y_telefon", "ФИО": "client_name", "!!!": "column"} {
		if got := sheets.Key(in); got != want {
			t.Errorf("Key(%q) = %q", in, got)
		}
	}
	csv := "Имя;Телефон;Заказ\nАли;90 123 45 67;стол\nВали;998901234567;стул\nБез номера;;шкаф\nГани;+998 93 000 00 01;\n"
	sheet, err := sheets.Read("list.csv", []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	column := sheet.PhoneColumn()
	columns, contacts, stats := sheet.Contacts(column)
	if column != 1 || len(columns) != 2 || columns[0].Key != "client_name" || columns[1].Key != "zakaz" {
		t.Fatalf("column %d, columns %+v", column, columns)
	}
	if stats.Rows != 4 || stats.Accepted != 2 || stats.Duplicates != 1 || stats.Invalid != 1 || contacts[0].Data["zakaz"] != "стол" || contacts[1].Row != 5 {
		t.Errorf("stats %+v contacts %+v", stats, contacts)
	}
}
