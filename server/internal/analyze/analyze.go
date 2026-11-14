// Package analyze reads a finished call's text without a language model: did
// the agent introduce itself as an AI assistant, did the person ask not to be
// called, did the agent promise to call again and when. The form's answers are filled by the person or their assistant.
package analyze

import (
	"context"
	"strings"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/compile"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
)

type Result struct {
	// The agent's first words named it an AI assistant.
	Disclosed bool
	// The person asked not to be called again.
	DoNotCall bool
	// The agent ended the call by promising to call again, and the moment it
	// named, if it named one.
	Callback   bool
	CallbackAt *time.Time
	Answers    map[string]model.Answer
	Summary    string
}

type Analyzer interface {
	Analyze(ctx context.Context, spec model.Spec, t *gapi.Transcript, now time.Time) (*Result, error)
}

// Baseline reads what needs no language model: the greeting, and the plain
// ways people say "do not call me" in Uzbek and Russian. It errs toward
// stopping: a number put on the stop list by mistake is taken off by a person.
type Baseline struct{}

var apostrophes = strings.NewReplacer("ʻ", "'", "‘", "'", "’", "'", "`", "'", "ʼ", "'", "ё", "е")

var stopPhrases = []string{
	"qo'ng'iroq qilmang", "qo'ng'iroq qilmanglar", "telefon qilmang", "telefon qilmanglar", "boshqa qo'ng'iroq qilma",
	"raqamimni o'chir", "nomerimni o'chir", "bezovta qilmang", "bezovta qilmanglar",
	"не звоните", "не звони", "больше не звон", "не надо звонить", "удалите мой номер", "удалите номер", "не беспокойте",
}

func (Baseline) Analyze(_ context.Context, _ model.Spec, t *gapi.Transcript, now time.Time) (*Result, error) {
	res := &Result{Disclosed: compile.Disclosed(t.Turns), Answers: map[string]model.Answer{}}
	res.Callback, res.CallbackAt = Callback(t.Turns, now)
	for _, turn := range t.Turns {
		if turn.Role != "caller" {
			continue
		}
		text := strings.ToLower(apostrophes.Replace(turn.Text))
		for _, p := range stopPhrases {
			if strings.Contains(text, p) {
				res.DoNotCall = true
			}
		}
	}
	return res, nil
}
