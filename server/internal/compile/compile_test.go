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
		agent, err := Build(tpl.Title, tpl.Spec, DefaultLimits, nil)
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
	agent, err := Build("a", spec, DefaultLimits, nil)
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
	greeting := model.Spec{Script: "1. {{studio_greeting}}"}
	own := model.Spec{Script: "1. «Men Malikaman.»\n3. «Men kompaniyaning sun'iy intellekt yordamchisiman. Boshlaymizmi?»"}
	check := func(name string, spec model.Spec, wantSaid, wantDue bool, turns ...gapi.Turn) {
		if said, due := Disclosed(spec, turns); said != wantSaid || due != wantDue {
			t.Errorf("%s: said %v, due %v", name, said, due)
		}
	}
	named := []gapi.Turn{turn("agent", "Alo?"), turn("agent", "Assalomu alaykum, men Malikaman."), turn("agent", "Bir daqiqa vaqtingiz bormi?"),
		turn("agent", "Men kompaniyaning sun'iy intellekt yordamchisiman. Boshlaymizmi?")}
	check("the Studio's greeting", greeting, true, true, turn("agent", "Alo?"), turn("caller", "Alo"), turn("agent", "Assalomu alaykum! Men «Olma» kompaniyasining sunʼiy intellekt yordamchisiman."))
	check("the greeting's script, a name first", greeting, false, true, named...)
	check("its own opening, a name first", own, true, false, named...)
	check("its own opening, the person left early", own, false, false, named[:3]...)
	check("a question about AI is not the words", own, false, false, turn("agent", "Alo?"), turn("agent", "Men Malikaman."), turn("agent", "Sun'iy intellekt haqidagi statistika sizga qiziqmi?"))
	check("the person's words do not count", greeting, false, true, turn("caller", "siz sun'iy intellekt yordamchisimisiz?"), turn("agent", "Yo'q."))
	late := []gapi.Turn{turn("agent", "Alo?")}
	for i := 0; i < 6; i++ {
		late = append(late, turn("agent", "Savol."), turn("agent", ""))
	}
	late = append(late, turn("agent", "Men sun'iy intellekt yordamchisiman."))
	check("its own opening, said too late", own, false, true, late...)
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

// A line marked with a condition is in the instruction only for the people it
// is for; the variable it reads is one a list's column fills.
func TestConditionalLines(t *testing.T) {
	spec := model.Spec{Company: "Namuna", Variables: map[string]string{"client_name": "—"}, Script: `Siz yordamchisiz.
{{if client_name!=—}}- Ismi: {{client_name}}.
1. Salom: {{studio_greeting}}
{{if segment=client|Наш  Клиент}}   Keyin: «Hamkorlik uchun rahmat!»
{{if segment!=client|наш клиент}}   Keyin: «Tanishganimdan xursandman.»
{{if segment=client}}{{if client_name!=—}}   «{{client_name}}, yana bir bor rahmat.»
2. Savol.`}
	if p := Check(spec, DefaultLimits); len(p) > 0 {
		t.Fatal(p)
	}
	if v := Variables(spec); len(v) != 2 || v[1] != "segment" {
		t.Fatalf("variables: %v", v)
	}
	for name, c := range map[string]struct {
		data     map[string]string
		in, out_ []string
	}{
		"a client by name": {map[string]string{"client_name": "Jasur", "segment": "наш клиент"}, []string{"- Ismi: {{client_name}}.", "  Keyin: «Hamkorlik uchun rahmat!»"}, []string{"Tanishganimdan", "{{if"}},
		"nobody we know":   {nil, []string{"Tanishganimdan"}, []string{"Ismi:", "Hamkorlik", "{{if"}},
		"another segment":  {map[string]string{"segment": "partner"}, []string{"Tanishganimdan"}, []string{"Hamkorlik"}},
		"the value's case": {map[string]string{"segment": " CLIENT "}, []string{"Hamkorlik"}, []string{"Tanishganimdan"}},
	} {
		agent, err := Build("a", spec, DefaultLimits, c.data)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range c.in {
			if !strings.Contains(agent.Prompt, want) {
				t.Errorf("%s: %q is missing", name, want)
			}
		}
		for _, not := range c.out_ {
			if strings.Contains(agent.Prompt, not) {
				t.Errorf("%s: %q is there", name, not)
			}
		}
		if _, ok := agent.Variables["segment"]; !ok {
			t.Errorf("%s: segment is not declared", name)
		}
	}
	both, _ := Build("a", spec, DefaultLimits, map[string]string{"client_name": "Jasur", "segment": "client"})
	one, _ := Build("a", spec, DefaultLimits, map[string]string{"segment": "client"})
	if !strings.Contains(both.Prompt, "yana bir bor rahmat") || strings.Contains(one.Prompt, "yana bir bor rahmat") {
		t.Error("two conditions in a row must both hold")
	}
	spec.Script = "Salom. {{if segment=client}} rahmat"
	if p := Check(spec, DefaultLimits); len(p) == 0 {
		t.Error("a condition in the middle of a line was accepted")
	}
}

// A script may say in its own opening that an AI assistant is calling; then
// the Studio does not force its greeting into the first sentence.
func TestOwnDisclosure(t *testing.T) {
	own := model.Spec{Company: "Olma", Script: "1. «Assalomu alaykum! Men Malikaman.»\n2. «Men kompaniyaning sunʼiy intellekt yordamchisiman. Boshlaymizmi?»"}
	agent, err := Build("a", own, DefaultLimits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(agent.Prompt, "birinchi gapingiz aynan shunday") || !strings.Contains(agent.Prompt, "har doim rostini ayting") {
		t.Errorf("a script with its own disclosure: %s", agent.Prompt)
	}
	none := model.Spec{Company: "Olma", Script: "1. «Assalomu alaykum! Men Malikaman.»"}
	if agent, _ := Build("a", none, DefaultLimits, nil); !strings.Contains(agent.Prompt, "birinchi gapingiz aynan shunday") {
		t.Error("a script that never says what the agent is did not get the greeting")
	}
}
