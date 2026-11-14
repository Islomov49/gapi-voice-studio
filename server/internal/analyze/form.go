package analyze

import (
	"errors"
	"strings"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
)

var plainer = strings.NewReplacer("ʻ", "'", "‘", "'", "’", "'", "`", "'", "ʼ", "'", "«", "", "»", "", "\"", "", ",", "", ".", "", "!", "", "?", "")

// Plain brings words to the form two spellings of the same speech share.
func Plain(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(plainer.Replace(s))), " ")
}

// Said is what was spoken in a call, line by line, for looking a quote up in.
func Said(turns []gapi.Turn) []string {
	said := make([]string, 0, len(turns))
	for _, t := range turns {
		if t.Role == "caller" || t.Role == "agent" {
			said = append(said, Plain(t.Text))
		}
	}
	return said
}

// Quoted reports whether the words stand in one line of the call.
func Quoted(said []string, quote string) bool {
	quote = Plain(quote)
	if quote == "" {
		return false
	}
	for _, line := range said {
		if strings.Contains(line, quote) {
			return true
		}
	}
	return false
}

// Fits checks an answer's value against its field's type.
func Fits(f model.Field, v any) error {
	switch f.Type {
	case "rating":
		if n, ok := v.(float64); !ok || n < 1 || n > 5 || n != float64(int(n)) {
			return errors.New("a rating is a whole number from 1 to 5")
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return errors.New("the value is a number")
		}
	case "yesno":
		if _, ok := v.(bool); !ok {
			return errors.New("the value is true or false")
		}
	case "choice":
		text, _ := v.(string)
		for _, o := range f.Options {
			if o == text {
				return nil
			}
		}
		return errors.New("the value is one of: " + strings.Join(f.Options, ", "))
	case "datetime":
		text, _ := v.(string)
		if _, err := time.Parse("2006-01-02T15:04", text); err != nil {
			if _, err := time.Parse("2006-01-02", text); err != nil {
				return errors.New("a date and time is written 2026-11-14T15:30, a date alone 2026-11-14")
			}
		}
	default:
		if text, ok := v.(string); !ok || len([]rune(text)) > 1000 {
			return errors.New("the value is a text of at most 1,000 characters")
		}
	}
	return nil
}
