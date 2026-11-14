// Package rules decides when a number may be dialled and what happens to a
// person after a call: the calling rules of the PRD as functions of the clock,
// with no database and no network, so that each can be tested by itself.
package rules

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
)

// Tashkent has no daylight saving: UTC+5 all year.
var Tashkent = time.FixedZone("Asia/Tashkent", 5*3600)

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a time like 09:00", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Check refuses rules a campaign may not run with.
func Check(r model.Rules) error {
	from, err := clock(r.From)
	if err != nil {
		return err
	}
	to, err := clock(r.To)
	if err != nil {
		return err
	}
	earliest, _ := clock(model.EarliestCall)
	latest, _ := clock(model.LatestCall)
	switch {
	case from < earliest || to > latest:
		return fmt.Errorf("calls are placed between %s and %s", model.EarliestCall, model.LatestCall)
	case to-from < 30:
		return errors.New("the calling hours are at least half an hour")
	case len(r.Days) == 0:
		return errors.New("choose at least one day of the week")
	case r.MaxAttempts < 1 || r.MaxAttempts > 5:
		return errors.New("a person is called 1 to 5 times")
	case r.RetryMinutes < 10 || r.RetryMinutes > 24*60:
		return errors.New("the pause between attempts is 10 minutes to a day")
	case r.FirstPortion < 1 || r.FirstPortion > 50:
		return errors.New("the first portion is 1 to 50 people")
	case r.Portion < 1 || r.Portion > 500:
		return errors.New("a portion is 1 to 500 people")
	}
	for _, d := range r.Days {
		if d < 1 || d > 7 {
			return errors.New("days are 1 (Monday) to 7 (Sunday)")
		}
	}
	return nil
}

func weekday(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

// Open reports whether a call may be placed at this moment.
func Open(r model.Rules, now time.Time) bool {
	now = now.In(Tashkent)
	from, err1 := clock(r.From)
	to, err2 := clock(r.To)
	if err1 != nil || err2 != nil {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	return slices.Contains(r.Days, weekday(now)) && m >= from && m < to
}

// NextOpen is the first moment at or after t when a call may be placed.
func NextOpen(r model.Rules, t time.Time) time.Time {
	if Open(r, t) {
		return t
	}
	from, err := clock(r.From)
	to, err2 := clock(r.To)
	if err != nil || err2 != nil || len(r.Days) == 0 {
		return t.Add(24 * time.Hour)
	}
	local := t.In(Tashkent)
	for i := 0; i < 8; i++ {
		day := local.AddDate(0, 0, i)
		start := time.Date(day.Year(), day.Month(), day.Day(), from/60, from%60, 0, 0, Tashkent)
		end := time.Date(day.Year(), day.Month(), day.Day(), to/60, to%60, 0, 0, Tashkent)
		if slices.Contains(r.Days, weekday(day)) && end.After(t) && !start.Before(t) {
			return start
		}
	}
	return t.Add(24 * time.Hour)
}

// ExtraCallbacks is how many calls beyond a campaign's attempts a person gets
// by asking the agent to call later.
const ExtraCallbacks = 2

// Decision is what becomes of a person after one call.
type Decision struct {
	// pending (call again at NextAt), done (talked), failed (not reached)
	Status string
	NextAt time.Time
	// What the results table shows: the call's outcome, or why nobody was reached.
	Result string
	// The call failed for a reason on our side or the provider's, not the person's.
	Technical bool
}

// Ended is what the dialer knows of a finished call.
type Ended struct {
	// Gapi's status: "ended" (the phone was answered) or "failed".
	Status    string
	EndReason string
	// The model's own word on how the talk ended: agreed, declined, callback, ...
	// It is a label for the results table. "callback" there also covers "a
	// manager will call you", so it does not by itself make the agent call again.
	Outcome string
	// The agent ended the call by promising to call again (analyze.Callback),
	// and the moment it named, if it named one.
	Callback   bool
	CallbackAt *time.Time
	// The person asked not to be called again.
	DoNotCall bool
}

// AfterCall decides a person's next step. attempts counts this call.
func AfterCall(r model.Rules, attempts int, c Ended, now time.Time) Decision {
	again := func(wait time.Duration, result string, technical bool) Decision {
		if attempts >= r.MaxAttempts {
			return Decision{Status: "failed", Result: result, Technical: technical}
		}
		return Decision{Status: "pending", NextAt: NextOpen(r, now.Add(wait)), Result: result, Technical: technical}
	}
	retry := time.Duration(r.RetryMinutes) * time.Minute
	if c.DoNotCall {
		return Decision{Status: "done", Result: "do_not_call"}
	}
	if c.Status == "ended" {
		if c.Callback || c.CallbackAt != nil {
			// A call the person asked for is placed past the attempts' limit,
			// ExtraCallbacks times at most; after that a person takes it over.
			if attempts >= r.MaxAttempts+ExtraCallbacks {
				return Decision{Status: "done", Result: "callback"}
			}
			at := now.Add(retry)
			if c.CallbackAt != nil && c.CallbackAt.After(now) {
				at = *c.CallbackAt
			}
			return Decision{Status: "pending", NextAt: NextOpen(r, at), Result: "call_later"}
		}
		result := c.Outcome
		if result == "" {
			result = c.EndReason
		}
		if result == "" {
			result = "talked"
		}
		return Decision{Status: "done", Result: result}
	}
	switch c.EndReason {
	case "busy", "no_answer":
		return again(retry, c.EndReason, false)
	case "declined":
		// Someone who rejected the call is tried once more, a day later.
		if attempts >= 2 {
			return Decision{Status: "failed", Result: "declined"}
		}
		return again(24*time.Hour, "declined", false)
	case "wrong_number":
		return Decision{Status: "failed", Result: "wrong_number"}
	default: // provider_refused, call_failed, no_agent, ...: not the person's doing
		reason := c.EndReason
		if reason == "" {
			reason = "call_failed"
		}
		return again(retry, reason, true)
	}
}

// Portion is what a finished portion of a campaign looked like.
type Portion struct {
	// Calls placed, and how many of them failed on our side or the provider's.
	Calls, Technical int
	// Calls that were answered; of those, how many asked not to be called and
	// how many the agent did not open as an AI assistant.
	Talked, DoNotCall, Undisclosed int
}

// Guard decides whether a campaign goes on to its next portion by itself or
// stops for a person to look: the reason, or "" to go on.
func Guard(p Portion) string {
	switch {
	case p.Calls >= 3 && p.Technical*10 >= p.Calls*3:
		return "technical_failures"
	case p.Talked >= 2 && p.DoNotCall*5 >= p.Talked:
		return "do_not_call_requests"
	case p.Talked >= 2 && p.Undisclosed*5 >= p.Talked:
		return "agent_did_not_introduce_itself"
	}
	return ""
}
