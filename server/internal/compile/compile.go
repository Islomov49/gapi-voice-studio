// Package compile turns a Studio agent version into the agent Gapi is called
// with: the script as written, plus the parts the Studio owns and nobody can
// leave out (the agent names itself an AI assistant, a "do not call me" ends
// the call, the form's questions).
package compile

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
)

// GreetingPlace is where a script puts the agent's first words. A script
// written from a Studio template has it inside its first stage, which is
// where the model keeps a rule best; a script without it gets the words as a
// rule of its own at the end.
const GreetingPlace = "{{studio_greeting}}"

// Greeting is the agent's first sentence on every call: the company, and that
// an AI assistant is calling. It is the Studio's, not the script's.
func Greeting(company string) string {
	return "Assalomu alaykum! Men «" + strings.TrimSpace(company) + "» kompaniyasining sun'iy intellekt yordamchisiman."
}

var (
	place   = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	braces  = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
	varName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	// A line for some of the people only: "{{if segment=client|partner}} text"
	// stays in the instruction, without the mark, when the person's value of
	// the variable is one of those listed; "!=" when it is none of them.
	// Several marks in a row at the start of a line must all hold.
	cond  = regexp.MustCompile(`^(\s*)\{\{\s*if\s+([a-z][a-z0-9_]{0,39})\s*(!?=)([^{}]*)\}\}[ \t]?`)
	anyIf = regexp.MustCompile(`\{\{\s*if\b[^{}]*\}\}`)
	// Filled in by Gapi at the start of a call, never declared.
	platformVars = map[string]bool{"current_date": true, "current_weekday": true, "current_time": true}
)

// Limits are Gapi's (GET /v1/voice/options).
type Limits struct {
	PromptChars     int
	Variables       int
	VocabularyTerms int
	MaxCallSeconds  int
}

// DefaultLimits are Gapi's as of 2026-11-13, for when its options were not read.
var DefaultLimits = Limits{PromptChars: 8000, Variables: 20, VocabularyTerms: 20, MaxCallSeconds: 1800}

// Problem is something in a version a call cannot be made with; Field names
// the part of the editor it belongs to.
type Problem struct {
	Field string `json:"field"`
	Text  string `json:"text"`
}

func (p Problem) Error() string { return p.Field + ": " + p.Text }

// Check lists everything that keeps a version from being called with.
func Check(s model.Spec, lim Limits) []Problem {
	var out []Problem
	add := func(field, text string) { out = append(out, Problem{field, text}) }
	if strings.TrimSpace(s.Company) == "" {
		add("company", "company name is empty: the agent names it in its first words")
	}
	if utf8.RuneCountInString(s.Company) > 80 || strings.ContainsAny(s.Company, "\n\r{}") {
		add("company", "company name is one line of at most 80 characters")
	}
	if strings.TrimSpace(s.Script) == "" {
		add("script", "the script is empty")
	}
	if s.Voice != "" && s.Voice != "charos" && s.Voice != "sarvar" {
		add("voice", "voice is charos or sarvar")
	}
	for name := range s.Variables {
		if !varName.MatchString(name) || platformVars[name] || name == "studio_greeting" {
			add("variables", fmt.Sprintf("variable %q: a name is a-z, 0-9 and _, starts with a letter, and is not one the platform fills in", name))
		}
	}
	for _, line := range strings.Split(s.Script, "\n") {
		for cond.MatchString(line) {
			line = cond.ReplaceAllString(line, "$1")
		}
		if anyIf.MatchString(line) {
			add("script", "a condition is written at the start of its line as {{if name=value}} or {{if name!=value}}, values separated by |")
			break
		}
	}
	for _, m := range braces.FindAllStringSubmatch(conditional(s.Script, nil, true), -1) {
		if name := strings.TrimSpace(m[1]); !varName.MatchString(name) {
			add("script", fmt.Sprintf("{{%s}}: a variable's name is a-z, 0-9 and _, starting with a letter", name))
		}
	}
	if n := len(declared(s)); n > lim.Variables {
		add("variables", fmt.Sprintf("%d variables, at most %d", n, lim.Variables))
	}
	if len(s.Vocabulary) > lim.VocabularyTerms {
		add("vocabulary", fmt.Sprintf("%d terms, at most %d", len(s.Vocabulary), lim.VocabularyTerms))
	}
	if s.MaxCallSeconds != 0 && (s.MaxCallSeconds < 30 || s.MaxCallSeconds > lim.MaxCallSeconds) {
		add("max_call_seconds", fmt.Sprintf("a call lasts 30 to %d seconds", lim.MaxCallSeconds))
	}
	seen := map[string]bool{}
	for i, f := range s.Form {
		where := fmt.Sprintf("form[%d]", i)
		switch {
		case !varName.MatchString(f.Key):
			add(where, "a field's key is a-z, 0-9 and _, starting with a letter")
		case seen[f.Key] || reserved[f.Key]:
			add(where, fmt.Sprintf("key %q is taken", f.Key))
		}
		seen[f.Key] = true
		if strings.TrimSpace(f.Title) == "" {
			add(where, "a field has a title")
		}
		switch f.Type {
		case "text", "number", "rating", "yesno", "datetime":
		case "choice":
			if len(f.Options) < 2 {
				add(where, "a choice has at least two options")
			}
		default:
			add(where, "type is text, number, choice, rating, yesno or datetime")
		}
		if strings.ContainsAny(f.Ask, "\n\r") {
			add(where, "the question is one line")
		}
	}
	if len(out) == 0 {
		if n := utf8.RuneCountInString(prompt(s, s.Variables, true)); n > lim.PromptChars {
			add("script", fmt.Sprintf("the instruction is %d characters with the Studio's rules added, at most %d", n, lim.PromptChars))
		}
	}
	return out
}

// The keys every call's form has besides the agent's own fields.
var reserved = map[string]bool{"outcome": true, "summary": true, "callback_at": true, "do_not_call": true}

// declared is every variable a call may fill: those the version lists and
// those its script uses, the latter with an empty default.
func declared(s model.Spec) map[string]string {
	out := map[string]string{}
	for k, v := range s.Variables {
		out[k] = v
	}
	for _, m := range place.FindAllStringSubmatch(conditional(s.Script, nil, true), -1) {
		name := m[1]
		if _, ok := out[name]; !ok && !platformVars[name] && name != "studio_greeting" {
			out[name] = ""
		}
	}
	for _, line := range strings.Split(s.Script, "\n") {
		for m := cond.FindStringSubmatch(line); m != nil; m = cond.FindStringSubmatch(line) {
			if _, ok := out[m[2]]; !ok && !platformVars[m[2]] {
				out[m[2]] = ""
			}
			line = m[1] + line[len(m[0]):]
		}
	}
	return out
}

// conditional keeps the script's lines that are for this person: a line
// marked {{if name=value}} stays, without its mark, when the person's value
// of the variable (a contact's column, or the variable's default) is among
// those listed. With all set every such line stays: the longest the
// instruction can be.
func conditional(script string, values map[string]string, all bool) string {
	if !strings.Contains(script, "{{") {
		return script
	}
	lines := strings.Split(script, "\n")
	kept := lines[:0:0]
	for _, line := range lines {
		keep := true
		for m := cond.FindStringSubmatch(line); m != nil; m = cond.FindStringSubmatch(line) {
			have := strings.ToLower(strings.Join(strings.Fields(values[m[2]]), " "))
			among := false
			for _, v := range strings.Split(m[4], "|") {
				if strings.ToLower(strings.Join(strings.Fields(v), " ")) == have {
					among = true
				}
			}
			if among != (m[3] == "=") {
				keep = false
			}
			line = m[1] + line[len(m[0]):]
		}
		if keep || all {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// person is the values a person's lines are chosen by: their own, and the
// variables' defaults where they have none.
func person(s model.Spec, data map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range s.Variables {
		out[k] = v
	}
	for k, v := range data {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out
}

// Variables lists the names a contact's columns may fill, sorted.
func Variables(s model.Spec) []string {
	d := declared(s)
	names := make([]string, 0, len(d))
	for k := range d {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// prompt is the instruction as the model will read it, with values in the
// variables' places: for measuring its length and for showing it.
func prompt(s model.Spec, values map[string]string, all bool) string {
	text := instruction(s, person(s, values), all)
	d := declared(s)
	return place.ReplaceAllStringFunc(text, func(m string) string {
		name := place.FindStringSubmatch(m)[1]
		if v, ok := values[name]; ok && v != "" {
			return v
		}
		if v, ok := d[name]; ok {
			return v
		}
		return m
	})
}

// Preview is the instruction with a contact's values filled in.
func Preview(s model.Spec, values map[string]string) string { return prompt(s, values, false) }

// CallbackRule has the agent settle a later call in words the Studio can read
// back from the call's text (analyze.Callback): the time repeated in a plain
// form, then "qayta qo'ng'iroq qilamiz", then goodbye.
const CallbackRule = "Mijoz hozir gaplasha olmasligini aytsa yoki keyinroq qo'ng'iroq qilishni so'rasa: savollarni davom ettirmang, qachon qulayligini so'rang. " +
	"Mijoz aytgan vaqtni o'z so'zlari bilan aniq takrorlang va «qayta qo'ng'iroq qilamiz» deng, masalan: «Xo'p, sizga yarim soatdan keyin qayta qo'ng'iroq qilamiz» yoki «Xo'p, sizga ertaga soat o'n birda qayta qo'ng'iroq qilamiz». " +
	"Vaqt aytmasa: «Xo'p, sizga keyinroq qayta qo'ng'iroq qilamiz» deng. Shundan keyin xayrlashing va suhbatni tugating."

// instruction is the script, with the lines that are for this person, plus
// the Studio's own rules; variables not filled.
func instruction(s model.Spec, values map[string]string, all bool) string {
	greeting := "«" + Greeting(s.Company) + "»"
	script := strings.TrimSpace(conditional(s.Script, values, all))
	var rules []string
	if strings.Contains(script, GreetingPlace) {
		script = strings.ReplaceAll(script, GreetingPlace, greeting)
	} else {
		rules = append(rules, "Mijoz javob bergach, birinchi gapingiz aynan shunday bo'lsin: "+greeting+" Shundan keyin nima uchun qo'ng'iroq qilayotganingizni bir jumlada ayting.")
	}
	rules = append(rules, "Mijoz «boshqa qo'ng'iroq qilmang» yoki «raqamimni o'chiring» desa: bir marta uzr so'rang va darrov xayrlashing, boshqa hech narsa taklif qilmang.")
	rules = append(rules, CallbackRule)
	var asks []string
	for _, f := range s.Form {
		if ask := strings.TrimSpace(f.Ask); ask != "" {
			asks = append(asks, ask)
		}
	}
	var b strings.Builder
	b.WriteString(script)
	b.WriteString("\n\nQo'shimcha qoidalar:")
	for _, r := range rules {
		b.WriteString("\n- " + r)
	}
	if len(asks) > 0 {
		b.WriteString("\n- Suhbat davomida quyidagilarni bilib oling. Har birini bir marta so'rang; mijoz o'zi aytgan bo'lsa, qayta so'ramang:")
		for _, a := range asks {
			b.WriteString("\n  - " + a)
		}
	}
	return b.String()
}

// Build is the agent Gapi is called with for this version and this person:
// data is the person's columns, which choose the conditional lines.
func Build(name string, s model.Spec, lim Limits, data map[string]string) (gapi.Agent, error) {
	if problems := Check(s, lim); len(problems) > 0 {
		return gapi.Agent{}, problems[0]
	}
	seconds := s.MaxCallSeconds
	if seconds == 0 {
		seconds = 300
	}
	voice := s.Voice
	if voice == "" {
		voice = "charos"
	}
	return gapi.Agent{
		Name:   name,
		Prompt: instruction(s, person(s, data), false),
		Voice:  voice,
		// A call the agent places: the person who picks up speaks first, and
		// Gapi opens with its own "Alo?" (measured there on 2026-11-12).
		FirstSpeaker:       "caller",
		Vocabulary:         s.Vocabulary,
		MaxDurationSeconds: seconds,
		Variables:          declared(s),
	}, nil
}

// Values picks from a contact's columns the ones the version's script uses,
// as Gapi takes them: one line each, no braces, not longer than a sentence.
func Values(s model.Spec, data map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for name := range declared(s) {
		v := strings.Join(strings.Fields(data[name]), " ")
		if v == "" {
			continue
		}
		if utf8.RuneCountInString(v) > 300 {
			return nil, errors.New("value of " + name + " is longer than 300 characters")
		}
		out[name] = strings.NewReplacer("{", "", "}", "").Replace(v)
	}
	return out, nil
}

var apostrophes = strings.NewReplacer("ʻ", "'", "‘", "'", "’", "'", "`", "'", "ʼ", "'")

// Disclosed reports whether the agent said it is an AI assistant in its first
// words: one of its first three lines holds the phrase of Greeting.
func Disclosed(turns []gapi.Turn) bool {
	seen := 0
	for _, t := range turns {
		if t.Role != "agent" {
			continue
		}
		if strings.Contains(strings.ToLower(apostrophes.Replace(t.Text)), "sun'iy intellekt") {
			return true
		}
		if seen++; seen == 3 {
			break
		}
	}
	return false
}
