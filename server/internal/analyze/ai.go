package analyze

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gemini"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
	"github.com/Islomov49/gapi-voice-studio/server/internal/rules"
)

// Reading is what a language model read in a call's text, after the Studio's
// own checks: an answer whose quote is not in the call or whose value does
// not fit its field is not here.
type Reading struct {
	Answers map[string]model.Answer
	Summary string
	// What a person should check by ear: answers the model was not sure of,
	// and answers the Studio threw out.
	Doubts []string
	// The agent promised to call again, as the model read it, and when.
	Callback   bool
	CallbackAt *time.Time
	DoNotCall  bool
}

// AI reads a call with the workspace's own Gemini key.
type AI struct {
	Gemini *gemini.Client
	Key    string
	Model  string
}

const aiSystem = `You read the text of one phone call. A company's AI voice agent ("agent") called a person ("caller"). They speak Uzbek, sometimes mixed with Russian. The caller's lines came through a phone line and a speech recogniser, so words in them may be misrecognised; the agent's lines are exact.

Fill the form from what the caller said.
- For every field the caller answered, give "key", "value" and "quote". "quote" is the caller's own words the value was taken from, copied character for character from one caller line of the text (a part of a line is enough). Do not translate or correct the quote.
- A field the caller did not answer is left out. Never take an answer from the agent's words, from the caller's tone, or from another answer. A short answer ("ha", "yo'q", "beshta") belongs to the question the agent asked just before it.
- "value" by the field's type: text = the answer's meaning as a short phrase in Russian; number = digits only; yesno = "true" or "false"; rating = a digit 1 to 5; choice = exactly one of the field's options; datetime = 2026-11-14T15:30.
- If a caller line looks misrecognised and you cannot tell the answer with confidence, leave the field out and say so in "doubts".

"summary": one or two sentences in Russian: what the call came to.
"doubts": what a person should check by listening to the recording, each a short sentence in Russian; an empty list when there is nothing.
"callback_promised": true only if, at the end of the call, the agent itself promised to call this person again later. A promise that a manager or another person will call is false.
"callback_time": when the agent promised to call, as Tashkent local time 2026-11-14T15:30, worked out from the current time given below; "" if no time was agreed.
"do_not_call": true only if the caller asked not to be called again.`

func aiSchema(form []model.Field) map[string]any {
	str := map[string]any{"type": "string"}
	key := map[string]any{"type": "string"}
	if len(form) > 0 {
		keys := make([]string, len(form))
		for i, f := range form {
			keys[i] = f.Key
		}
		key["enum"] = keys
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"answers": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{"key": key, "value": str, "quote": str}, "required": []string{"key", "value", "quote"},
			}},
			"summary":           str,
			"doubts":            map[string]any{"type": "array", "items": str},
			"callback_promised": map[string]any{"type": "boolean"},
			"callback_time":     str,
			"do_not_call":       map[string]any{"type": "boolean"},
		},
		"required": []string{"answers", "summary", "doubts", "callback_promised", "callback_time", "do_not_call"},
	}
}

func aiInput(spec model.Spec, turns []gapi.Turn, now time.Time) string {
	var b strings.Builder
	local := now.In(rules.Tashkent)
	fmt.Fprintf(&b, "Current time in Tashkent: %s, %s.\n\nForm fields (key | title | type | options):\n", local.Format("2006-01-02T15:04"), local.Weekday())
	if len(spec.Form) == 0 {
		b.WriteString("(the form has no fields: answer with an empty list)\n")
	}
	for _, f := range spec.Form {
		fmt.Fprintf(&b, "- %s | %s | %s | %s\n", f.Key, f.Title, f.Type, strings.Join(f.Options, "; "))
	}
	b.WriteString("\nThe call:\n")
	for _, t := range turns {
		if (t.Role == "agent" || t.Role == "caller") && strings.TrimSpace(t.Text) != "" {
			fmt.Fprintf(&b, "%s: %s\n", t.Role, t.Text)
		}
	}
	return b.String()
}

// typed turns the model's text into the value the field's type takes.
func typed(f model.Field, text string) (any, bool) {
	text = strings.TrimSpace(text)
	switch f.Type {
	case "number", "rating":
		n, err := strconv.ParseFloat(strings.ReplaceAll(text, ",", "."), 64)
		return n, err == nil
	case "yesno":
		switch strings.ToLower(text) {
		case "true", "yes", "да", "ha":
			return true, true
		case "false", "no", "нет", "yo'q":
			return false, true
		}
		return nil, false
	}
	return text, text != ""
}

// Read asks the model and keeps what stands the Studio's checks.
func (a AI) Read(ctx context.Context, spec model.Spec, turns []gapi.Turn, now time.Time) (*Reading, error) {
	var out struct {
		Answers []struct {
			Key, Value, Quote string
		} `json:"answers"`
		Summary          string   `json:"summary"`
		Doubts           []string `json:"doubts"`
		CallbackPromised bool     `json:"callback_promised"`
		CallbackTime     string   `json:"callback_time"`
		DoNotCall        bool     `json:"do_not_call"`
	}
	if err := a.Gemini.JSON(ctx, a.Key, a.Model, aiSystem, aiInput(spec, turns, now), aiSchema(spec.Form), &out); err != nil {
		return nil, err
	}
	res := &Reading{Answers: map[string]model.Answer{}, Callback: out.CallbackPromised, DoNotCall: out.DoNotCall}
	fields := map[string]model.Field{}
	for _, f := range spec.Form {
		fields[f.Key] = f
	}
	said := Said(turns)
	for _, ans := range out.Answers {
		f, ok := fields[ans.Key]
		if !ok {
			continue
		}
		value, ok := typed(f, ans.Value)
		if ok && Fits(f, value) != nil {
			ok = false
		}
		switch {
		case !ok:
			res.Doubts = append(res.Doubts, fmt.Sprintf("«%s»: ответ модели «%s» не подходит полю, не записан.", f.Title, clip(ans.Value, 80)))
		case !Quoted(said, ans.Quote):
			res.Doubts = append(res.Doubts, fmt.Sprintf("«%s»: модель ответила «%s», но таких слов в тексте звонка нет, не записано.", f.Title, clip(ans.Value, 80)))
		default:
			res.Answers[ans.Key] = model.Answer{Value: value, Quote: strings.TrimSpace(ans.Quote)}
		}
	}
	for _, d := range out.Doubts {
		if d = strings.TrimSpace(d); d != "" && len(res.Doubts) < 12 {
			res.Doubts = append(res.Doubts, clip(d, 300))
		}
	}
	res.Summary = clip(strings.TrimSpace(out.Summary), 600)
	if res.Callback && out.CallbackTime != "" {
		if at, err := time.ParseInLocation("2006-01-02T15:04", out.CallbackTime, rules.Tashkent); err == nil && at.After(now) && at.Sub(now) <= farthest {
			res.CallbackAt = &at
		}
	}
	return res, nil
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
