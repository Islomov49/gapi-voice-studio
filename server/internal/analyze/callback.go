package analyze

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/rules"
)

// A call back is read from the agent's own words, not the person's: the
// agent's text is exact, the person's went through a phone line and a
// recogniser. The Studio's rule (compile.instruction) has the agent repeat
// the time in a plain form before it says goodbye, and that line is read here.

// The agent itself will call: a first-person "we/I will call".
var callsAgain = []string{"qo'ng'iroq qilamiz", "qo'ng'iroq qilaman", "telefon qilamiz", "telefon qilaman"}

var (
	notWord = regexp.MustCompile(`[^\p{L}\p{N}':]+`)
	clock   = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
	digits  = regexp.MustCompile(`^\d{1,3}$`)
)

var units = map[string]int{"bir": 1, "ikki": 2, "uch": 3, "to'rt": 4, "besh": 5, "olti": 6, "yetti": 7, "sakkiz": 8, "to'qqiz": 9}
var tens = map[string]int{"o'n": 10, "yigirma": 20, "o'ttiz": 30, "qirq": 40, "ellik": 50, "oltmish": 60}

var weekdays = map[string]time.Weekday{
	"dushanba": time.Monday, "seshanba": time.Tuesday, "chorshanba": time.Wednesday, "payshanba": time.Thursday,
	"juma": time.Friday, "shanba": time.Saturday, "yakshanba": time.Sunday,
}

// The furthest a call back is put off; a later date is somebody's plan, not a call back.
const farthest = 7 * 24 * time.Hour

// Callback reports whether the agent ended the call by promising to call
// again, and the moment it named, if it named one that is still ahead.
func Callback(turns []gapi.Turn, now time.Time) (bool, *time.Time) {
	seen := 0
	for i := len(turns) - 1; i >= 0 && seen < 3; i-- {
		if turns[i].Role != "agent" || strings.TrimSpace(turns[i].Text) == "" {
			continue
		}
		seen++
		text := strings.ToLower(apostrophes.Replace(turns[i].Text))
		verb := -1
		for _, v := range callsAgain {
			if at := strings.Index(text, v); at >= 0 && (verb < 0 || at < verb) {
				verb = at
			}
		}
		if verb < 0 {
			continue
		}
		words := strings.Fields(notWord.ReplaceAllString(text[:verb], " "))
		if at, ok := moment(words, now); ok {
			return true, &at
		}
		if strings.Contains(text[:verb], "qayta") || strings.Contains(text[:verb], "keyinroq") {
			return true, nil
		}
	}
	return false, nil
}

// stem takes the case endings off a word of a time: "uchda" is "uch".
func stem(w string) string {
	if _, ok := units[w]; ok {
		return w
	}
	if _, ok := tens[w]; ok {
		return w
	}
	for _, end := range []string{"larda", "lardan", "dan", "da", "ga", "lar"} {
		if s, ok := strings.CutSuffix(w, end); ok && s != "" {
			return s
		}
	}
	return w
}

// number reads a number that starts at words[i]: digits, or Uzbek words up
// to 69. It answers the number and how many words it took.
func number(words []string, i int) (int, int) {
	if i >= len(words) {
		return 0, 0
	}
	w := stem(words[i])
	if digits.MatchString(w) {
		n, _ := strconv.Atoi(w)
		return n, 1
	}
	if t, ok := tens[w]; ok {
		// "o'n besh" is one number; "o'n beshda" too, and "o'nda besh" is not
		if words[i] == w && i+1 < len(words) {
			if u, ok := units[stem(words[i+1])]; ok {
				return t + u, 2
			}
		}
		return t, 1
	}
	if u, ok := units[w]; ok {
		return u, 1
	}
	return 0, 0
}

// before reads the number that ends just before words[i].
func before(words []string, i int) (int, bool) {
	for _, back := range []int{2, 1} {
		if i-back < 0 {
			continue
		}
		if n, took := number(words, i-back); took == back {
			return n, true
		}
	}
	return 0, false
}

func moment(words []string, now time.Time) (time.Time, bool) {
	ahead := func(t time.Time) (time.Time, bool) { return t, t.After(now) && t.Sub(now) <= farthest }
	has := func(w string) bool {
		for _, x := range words {
			if x == w {
				return true
			}
		}
		return false
	}
	// "o'n daqiqadan keyin", "ikki soatdan keyin", "bir yarim soatdan keyin"
	for i, w := range words {
		if i+1 >= len(words) || (words[i+1] != "keyin" && words[i+1] != "so'ng") {
			continue
		}
		switch w {
		case "daqiqadan", "minutdan", "daqiqalardan":
			if n, ok := before(words, i); ok && n > 0 {
				return ahead(now.Add(time.Duration(n) * time.Minute))
			}
		case "soatdan", "soatlardan":
			if i > 0 && words[i-1] == "yarim" {
				n, _ := before(words, i-1)
				return ahead(now.Add(time.Duration(n*60+30) * time.Minute))
			}
			if n, ok := before(words, i); ok && n > 0 {
				return ahead(now.Add(time.Duration(n) * time.Hour))
			}
		}
	}
	local := now.In(rules.Tashkent)
	days, named := 0, false
	switch {
	case has("ertaga"):
		days, named = 1, true
	case has("indinga"):
		days, named = 2, true
	case has("bugun"):
		named = true
	default:
		for _, w := range words {
			if d, ok := weekdays[stem(w)]; ok {
				days = (int(d) - int(local.Weekday()) + 7) % 7
				if days == 0 {
					days = 7
				}
				named = true
			}
		}
	}
	morning := has("ertalab") || has("ertalabki")
	evening := has("kechqurun") || has("kechki") || has("kechga") || has("kechroq")
	afternoon := has("kunduzi") || has("peshin") || has("peshinda")
	for i, w := range words {
		if w == "tushdan" && i+1 < len(words) && words[i+1] == "keyin" {
			afternoon = true
		}
	}
	day := func(hour, minute int) time.Time {
		d := local.AddDate(0, 0, days)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, rules.Tashkent)
	}
	// "soat uchda", "soat o'n besh o'ttizda", "soat uch yarimda", "soat 15:30 da"
	for i, w := range words {
		if w != "soat" || i+1 >= len(words) {
			continue
		}
		hour, minute := 0, 0
		if m := clock.FindStringSubmatch(stem(words[i+1])); m != nil {
			hour, _ = strconv.Atoi(m[1])
			minute, _ = strconv.Atoi(m[2])
		} else {
			n, took := number(words, i+1)
			if took == 0 {
				continue
			}
			hour = n
			if j := i + 1 + took; j < len(words) {
				if stem(words[j]) == "yarim" {
					minute = 30
				} else if m, took := number(words, j); took > 0 && m < 60 {
					minute = m
				}
			}
		}
		if hour < 1 || hour > 23 || minute > 59 {
			return time.Time{}, false
		}
		// Nobody asks an agent to call at three at night: an hour below eight
		// is the afternoon's unless the morning was named.
		if hour < 8 && !morning {
			hour += 12
		} else if hour >= 8 && hour < 12 && evening {
			hour += 12
		}
		t := day(hour, minute)
		if !t.After(now) && !named {
			if hour < 12 && !morning && !day(hour+12, minute).Before(now) {
				t = day(hour+12, minute) // "soat to'qqizda" said at two in the afternoon
			} else {
				t = t.AddDate(0, 0, 1)
			}
		}
		return ahead(t)
	}
	if !named {
		return time.Time{}, false
	}
	// a day with no hour: its morning, afternoon or evening; a bare "ertaga"
	// is that day's first calling minute, which the campaign's hours decide
	switch {
	case morning:
		return ahead(day(9, 0))
	case afternoon:
		return ahead(day(14, 0))
	case evening:
		return ahead(day(18, 0))
	case days > 0:
		return ahead(day(0, 0))
	}
	return time.Time{}, false
}
