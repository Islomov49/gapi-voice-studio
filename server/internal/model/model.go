// Package model holds what the Studio's parts pass to each other: an agent's
// description, a form, a campaign's rules.
package model

// Spec is one version of an agent: what the Studio turns into the instruction
// Gapi's model reads, plus the form the call should fill.
type Spec struct {
	// The business's name as people say it: the agent names it, and itself as
	// its AI assistant, in its first words.
	Company string `json:"company"`
	// The instruction, in the callers' language, with {{variable}} places.
	Script string `json:"script"`
	// charos or sarvar.
	Voice string `json:"voice"`
	// The script's variables with their defaults; a contact's columns fill them.
	Variables map[string]string `json:"variables,omitempty"`
	// Names and terms the recogniser should expect.
	Vocabulary []string `json:"vocabulary,omitempty"`
	// The longest a call may run.
	MaxCallSeconds int `json:"max_call_seconds"`
	// What the call should find out.
	Form []Field `json:"form,omitempty"`
}

// Field is one answer a call collects.
type Field struct {
	// The key the answer is kept under: [a-z][a-z0-9_]*.
	Key string `json:"key"`
	// What people see as the column's title.
	Title string `json:"title"`
	// text, number, choice, rating, yesno, datetime
	Type string `json:"type"`
	// For choice: the allowed values.
	Options []string `json:"options,omitempty"`
	// How the agent asks for it, in the callers' language; empty = the agent
	// does not ask, the answer is read from the conversation if it is there.
	Ask      string `json:"ask,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Rules are a campaign's calling rules. The server checks every one of them
// before each dial; nothing in the interface or the assistant switches them off.
type Rules struct {
	// Calls are placed between these hours, Tashkent time, "09:00" to "20:00".
	From string `json:"from"`
	To   string `json:"to"`
	// Days of the week calls are placed on: 1 = Monday ... 7 = Sunday.
	Days []int `json:"days"`
	// How many times a person is called at most, and how long to wait after
	// a call that was not answered.
	MaxAttempts  int `json:"max_attempts"`
	RetryMinutes int `json:"retry_minutes"`
	// The first portion and the ones after it, in people.
	FirstPortion int `json:"first_portion"`
	Portion      int `json:"portion"`
}

// DefaultRules is what a new campaign starts with (PRD, "Правила обзвона").
func DefaultRules() Rules {
	return Rules{From: "09:00", To: "20:00", Days: []int{1, 2, 3, 4, 5, 6}, MaxAttempts: 3, RetryMinutes: 120, FirstPortion: 10, Portion: 30}
}

// The outer bounds no campaign's hours may leave.
const (
	EarliestCall = "08:00"
	LatestCall   = "21:00"
)

// Answer is one field of a call's form as it was read from the call's text.
type Answer struct {
	Value any `json:"value"`
	// The caller's words the value was taken from.
	Quote string `json:"quote,omitempty"`
	// A person corrected the value.
	Edited bool `json:"edited,omitempty"`
}
