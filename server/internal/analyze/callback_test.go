package analyze

import (
	"testing"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/rules"
)

func TestCallback(t *testing.T) {
	// Tuesday 2026-11-17, 14:20 in Tashkent
	now := time.Date(2026, 11, 17, 14, 20, 0, 0, rules.Tashkent)
	on := func(day, hour, minute int) string {
		return time.Date(2026, 11, day, hour, minute, 0, 0, rules.Tashkent).Format("02 15:04")
	}
	for line, want := range map[string]string{
		"Xo'p, sizga o'n daqiqadan keyin qayta qo'ng'iroq qilamiz. Xayr!":    on(17, 14, 30),
		"Xo‘p, sizga yigirma besh daqiqadan keyin qayta qo‘ng‘iroq qilamiz.": on(17, 14, 45),
		"Yaxshi, 40 minutdan keyin qo'ng'iroq qilaman.":                      on(17, 15, 0),
		"Xo'p, sizga yarim soatdan keyin qayta qo'ng'iroq qilamiz.":          on(17, 14, 50),
		"Xo'p, bir yarim soatdan so'ng qayta qo'ng'iroq qilamiz.":            on(17, 15, 50),
		"Xo'p, sizga ikki soatdan keyin qayta qo'ng'iroq qilamiz.":           on(17, 16, 20),
		"Xo'p, sizga bugun soat beshda qayta qo'ng'iroq qilamiz.":            on(17, 17, 0),
		"Xo'p, soat uch yarimda qayta qo'ng'iroq qilamiz.":                   on(17, 15, 30),
		"Xo'p, soat o'n besh o'ttizda qayta qo'ng'iroq qilamiz.":             on(17, 15, 30),
		"Xo'p, soat 16:45 da qayta qo'ng'iroq qilamiz.":                      on(17, 16, 45),
		"Xo'p, soat ikkida qayta qo'ng'iroq qilamiz.":                        on(18, 14, 0), // two has passed today
		"Xo'p, soat to'qqizda qayta qo'ng'iroq qilamiz.":                     on(17, 21, 0),
		"Xo'p, sizga ertaga soat o'n birda qayta qo'ng'iroq qilamiz.":        on(18, 11, 0),
		"Xo'p, ertaga ertalab soat to'qqizda qayta qo'ng'iroq qilamiz.":      on(18, 9, 0),
		"Xo'p, ertaga kechqurun soat yettilarda qayta qo'ng'iroq qilamiz.":   on(18, 19, 0),
		"Xo'p, sizga ertaga qayta qo'ng'iroq qilamiz.":                       on(18, 0, 0),
		"Xo'p, ertaga tushdan keyin qayta qo'ng'iroq qilamiz.":               on(18, 14, 0),
		"Xo'p, bugun kechqurun qayta qo'ng'iroq qilamiz.":                    on(17, 18, 0),
		"Xo'p, indinga soat uchda qayta qo'ng'iroq qilamiz.":                 on(19, 15, 0),
		"Xo'p, juma kuni soat o'n ikkida qayta qo'ng'iroq qilamiz.":          on(20, 12, 0),
		"Xo'p, sizga keyinroq qayta qo'ng'iroq qilamiz.":                     "no time",
		"Xo'p, sizga qayta qo'ng'iroq qilamiz, xayr.":                        "no time",
		"Uzr so'raymiz. Rahbarimiz bugun sizga qo'ng'iroq qiladi.":           "",
		"Menejerimiz ertaga soat o'nda siz bilan bog'lanadi.":                "",
		"Rahmat, fikringiz uchun! Xayr.":                                     "",
		"Agar savol bo'lsa, bizga qo'ng'iroq qilishingiz mumkin.":            "",
		"Kerak bo'lsa, o'zimiz qo'ng'iroq qilamiz.":                          "",
		"Xo'p, sizga uch oydan keyin qayta qo'ng'iroq qilamiz.":              "no time",
		"Xo'p, sizga o'ttiz birinchi dekabrda qayta qo'ng'iroq qilamiz.":     "no time",
	} {
		turns := []gapi.Turn{{Role: "agent", Text: "Assalomu alaykum!"}, {Role: "caller", Text: "Hozir bandman."}, {Role: "agent", Text: line}, {Role: "caller", Text: "Xo'p."}}
		asked, at := Callback(turns, now)
		got := ""
		switch {
		case asked && at != nil:
			got = at.In(rules.Tashkent).Format("02 15:04")
		case asked:
			got = "no time"
		}
		if got != want {
			t.Errorf("%q: %q, want %q", line, got, want)
		}
	}
	// a promise the talk went on past is not how the call ended
	turns := []gapi.Turn{{Role: "agent", Text: "Xo'p, sizga o'n daqiqadan keyin qayta qo'ng'iroq qilamiz."}, {Role: "caller", Text: "Mayli, hozir gaplashaylik."},
		{Role: "agent", Text: "Yaxshi."}, {Role: "agent", Text: "Birinchi savol."}, {Role: "agent", Text: "Rahmat, xayr!"}}
	if asked, _ := Callback(turns, now); asked {
		t.Error("an old promise read as the call's end")
	}
}
