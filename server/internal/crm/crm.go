// Package crm connects the Studio to a business's CRM (Bitrix24, amoCRM): it
// reads the cards at a stage of a pipeline as people to call, and writes what
// a call came to back onto the card. The Studio asks the CRM; the CRM never
// has to reach the Studio, so nothing here needs a public address.
package crm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
)

type Stage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Pipeline is a funnel of deals or the one list of lead statuses.
type Pipeline struct {
	// deal or lead
	Entity string  `json:"entity"`
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Stages []Stage `json:"stages"`
}

// Card is a deal or a lead with the person to call.
type Card struct {
	Entity string
	ID     string
	Title  string
	// The person's name and phone: the card's contact, or the card itself.
	Name  string
	Phone string
	// Who in the business is responsible for the card: tasks go to them.
	Responsible string
	// What else the card says, for the script's variables: title, amount.
	Fields map[string]string
}

type Connector interface {
	// Account checks the credentials and names the account they open.
	Account(ctx context.Context) (string, error)
	Pipelines(ctx context.Context) ([]Pipeline, error)
	// Cards lists the cards at a stage, newest first, at most limit.
	Cards(ctx context.Context, entity, pipelineID, stageID string, limit int) ([]Card, error)
	// Note writes a comment onto a card.
	Note(ctx context.Context, entity, id, text string) error
	// Move puts a card on another stage.
	Move(ctx context.Context, entity, id, pipelineID, stageID string) error
	// Task sets a task about a card for a person of the business.
	Task(ctx context.Context, entity, id, responsible, text string, due time.Time) error
	// Responsible is who the card belongs to now: the one a task goes to.
	Responsible(ctx context.Context, entity, id string) (string, error)
}

// Open makes the connector of a kept connection.
func Open(kind, base, secret string) (Connector, error) {
	switch kind {
	case "bitrix24":
		return NewBitrix24(base, secret), nil
	case "amocrm":
		return NewAmoCRM(base, secret), nil
	}
	return nil, fmt.Errorf("no connector for %q", kind)
}

// Ref names a card: "bitrix24:deal:123".
func Ref(kind, entity, id string) string { return kind + ":" + entity + ":" + id }

func ParseRef(ref string) (kind, entity, id string, ok bool) {
	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// StageRef is a stage of a pipeline.
type StageRef struct {
	PipelineID string `json:"pipeline_id"`
	StageID    string `json:"stage_id"`
}

// Rules are what a campaign does in the CRM. Results are a person's final
// standing in the campaign: agreed, declined, callback, resolved, do_not_call,
// no_answer, busy, wrong_number, ... ; "*" stands for any result not named.
type Rules struct {
	ConnectionID string `json:"connection_id"`
	// Write a note onto the card when the person's calls are over.
	Note bool `json:"note"`
	// The stage a card moves to, by result.
	Move map[string]StageRef `json:"move,omitempty"`
	// The task set for the card's responsible person, by result: its words.
	Task map[string]string `json:"task,omitempty"`
	// While the campaign runs, cards that arrive at this stage join it.
	Feed *Feed `json:"feed,omitempty"`
}

type Feed struct {
	Entity     string `json:"entity"`
	PipelineID string `json:"pipeline_id"`
	StageID    string `json:"stage_id"`
}

// For picks a rule's value for a result: the result's own, else "*".
func For[T any](by map[string]T, result string) (T, bool) {
	if v, ok := by[result]; ok {
		return v, true
	}
	v, ok := by["*"]
	return v, ok
}

var results = map[string]string{
	"agreed": "согласие", "resolved": "вопрос решён", "declined": "отказ", "callback": "нужен звонок менеджера", "lost_interest": "потерял интерес",
	"time_up": "вышло время разговора", "caller_left": "положил трубку", "talked": "разговор состоялся", "do_not_call": "просил больше не звонить",
	"no_answer": "не ответил", "busy": "занято", "wrong_number": "неверный номер", "stop_list": "номер в стоп-листе", "provider_refused": "оператор связи не принял звонок",
	"call_failed": "сбой звонка", "agent_left": "агент завершил разговор",
}

// Result is a result's name as a card's note says it.
func Result(code string) string {
	if s, ok := results[code]; ok {
		return s
	}
	return code
}

// CallNote is the note written onto a card when the person's calls are over.
func CallNote(agent, result string, attempts int, seconds float64, summary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Голосовой агент Gapi «%s»\nИтог: %s", agent, Result(result))
	if attempts > 1 {
		fmt.Fprintf(&b, " (попыток: %d)", attempts)
	}
	if seconds >= 1 {
		fmt.Fprintf(&b, "\nРазговор: %d:%02d", int(seconds)/60, int(seconds)%60)
	}
	if summary = strings.TrimSpace(summary); summary != "" {
		b.WriteString("\nКратко: " + summary)
	}
	return b.String()
}

// FormNote is the note with the answers a call's form holds.
func FormNote(agent string, form []model.Field, answers map[string]model.Answer, summary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Голосовой агент Gapi «%s»: ответы", agent)
	if summary = strings.TrimSpace(summary); summary != "" {
		b.WriteString("\nКратко: " + summary)
	}
	for _, f := range form {
		a, ok := answers[f.Key]
		if !ok || a.Value == nil {
			continue
		}
		value := fmt.Sprint(a.Value)
		switch a.Value {
		case true:
			value = "да"
		case false:
			value = "нет"
		}
		fmt.Fprintf(&b, "\n%s: %s", f.Title, value)
	}
	return b.String()
}

// Variables are what a card gives a script: the person's name as client_name
// and the card's own fields.
func Variables(c Card) map[string]string {
	out := map[string]string{}
	if c.Name != "" {
		out["client_name"] = c.Name
	}
	if c.Title != "" {
		out["title"] = c.Title
	}
	for k, v := range c.Fields {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// Columns lists the variables a set of cards gives, for a list's header.
func Columns(cards []Card) []string {
	seen := map[string]bool{}
	for _, c := range cards {
		for k := range Variables(c) {
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
