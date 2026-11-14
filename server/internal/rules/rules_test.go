package rules

import (
	"testing"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
)

func at(day int, clock string) time.Time { // November 2026: the 16th is a Monday
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-"+twoDigits(day)+" "+clock, Tashkent)
	return t
}

func twoDigits(n int) string { return string([]byte{byte('0' + n/10), byte('0' + n%10)}) }

func TestHours(t *testing.T) {
	r := model.DefaultRules() // 09:00-20:00, Monday to Saturday
	for _, c := range []struct {
		when time.Time
		open bool
		next time.Time
	}{
		{at(16, "08:59"), false, at(16, "09:00")},
		{at(16, "09:00"), true, at(16, "09:00")},
		{at(16, "19:59"), true, at(16, "19:59")},
		{at(16, "20:00"), false, at(17, "09:00")},
		{at(21, "23:30"), false, at(23, "09:00")}, // Saturday night: Sunday is skipped
		{at(22, "12:00"), false, at(23, "09:00")}, // Sunday
	} {
		if got := Open(r, c.when); got != c.open {
			t.Errorf("Open(%s) = %v", c.when.Format("Mon 15:04"), got)
		}
		if got := NextOpen(r, c.when); !got.Equal(c.next) {
			t.Errorf("NextOpen(%s) = %s, want %s", c.when.Format("Mon 15:04"), got.Format("Mon 15:04"), c.next.Format("Mon 15:04"))
		}
	}
	// the same moment written in another zone is the same moment
	if !Open(r, at(16, "10:00").UTC()) {
		t.Error("10:00 in Tashkent given as UTC should be open")
	}
}

func TestCheck(t *testing.T) {
	ok := model.DefaultRules()
	if err := Check(ok); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*model.Rules){
		"before the earliest hour": func(r *model.Rules) { r.From = "07:00" },
		"after the latest hour":    func(r *model.Rules) { r.To = "22:00" },
		"no days":                  func(r *model.Rules) { r.Days = nil },
		"too many attempts":        func(r *model.Rules) { r.MaxAttempts = 9 },
		"a first portion too big":  func(r *model.Rules) { r.FirstPortion = 500 },
		"not a time":               func(r *model.Rules) { r.From = "morning" },
	} {
		r := ok
		change(&r)
		if Check(r) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAfterCall(t *testing.T) {
	r := model.DefaultRules() // 3 attempts, 120 minutes apart
	now := at(16, "19:30")
	callback := at(18, "15:00")
	for name, c := range map[string]struct {
		attempts int
		call     Ended
		status   string
		result   string
		next     time.Time
	}{
		"talked":                          {1, Ended{Status: "ended", Outcome: "agreed"}, "done", "agreed", time.Time{}},
		"no answer, first":                {1, Ended{Status: "failed", EndReason: "no_answer"}, "pending", "no_answer", at(17, "09:00")}, // 21:30 is closed
		"no answer, last":                 {3, Ended{Status: "failed", EndReason: "no_answer"}, "failed", "no_answer", time.Time{}},
		"declined once":                   {1, Ended{Status: "failed", EndReason: "declined"}, "pending", "declined", at(17, "19:30")},
		"declined twice":                  {2, Ended{Status: "failed", EndReason: "declined"}, "failed", "declined", time.Time{}},
		"wrong number":                    {1, Ended{Status: "failed", EndReason: "wrong_number"}, "failed", "wrong_number", time.Time{}},
		"provider refused":                {1, Ended{Status: "failed", EndReason: "provider_refused"}, "pending", "provider_refused", at(17, "09:00")},
		"the agent will call at a time":   {1, Ended{Status: "ended", Outcome: "callback", Callback: true, CallbackAt: &callback}, "pending", "call_later", callback},
		"the agent will call, no time":    {1, Ended{Status: "ended", Callback: true}, "pending", "call_later", at(17, "09:00")},
		"asked past the attempts":         {3, Ended{Status: "ended", Callback: true, CallbackAt: &callback}, "pending", "call_later", callback},
		"asked too many times":            {5, Ended{Status: "ended", Callback: true, CallbackAt: &callback}, "done", "callback", time.Time{}},
		"a manager will call, says model": {1, Ended{Status: "ended", Outcome: "callback"}, "done", "callback", time.Time{}},
		"do not call":                     {1, Ended{Status: "ended", Outcome: "callback", Callback: true, DoNotCall: true}, "done", "do_not_call", time.Time{}},
	} {
		d := AfterCall(r, c.attempts, c.call, now)
		if d.Status != c.status || d.Result != c.result || (c.status == "pending" && !d.NextAt.Equal(c.next)) {
			t.Errorf("%s: %s %s at %s", name, d.Status, d.Result, d.NextAt.Format("Mon 15:04"))
		}
	}
	if !AfterCall(r, 1, Ended{Status: "failed", EndReason: "call_failed"}, now).Technical {
		t.Error("a failed call that is not the person's doing is technical")
	}
}

func TestGuard(t *testing.T) {
	for name, c := range map[string]struct {
		p    Portion
		want string
	}{
		"a clean portion":           {Portion{Calls: 10, Talked: 6}, ""},
		"one technical failure":     {Portion{Calls: 10, Technical: 1, Talked: 5}, ""},
		"a third fail technically":  {Portion{Calls: 10, Technical: 3, Talked: 5}, "technical_failures"},
		"people ask not to be rung": {Portion{Calls: 10, Talked: 5, DoNotCall: 1}, "do_not_call_requests"},
		"the agent does not say it": {Portion{Calls: 10, Talked: 5, Undisclosed: 2}, "agent_did_not_introduce_itself"},
		"too few calls to judge":    {Portion{Calls: 2, Technical: 2}, ""},
	} {
		if got := Guard(c.p); got != c.want {
			t.Errorf("%s: %q", name, got)
		}
	}
}
