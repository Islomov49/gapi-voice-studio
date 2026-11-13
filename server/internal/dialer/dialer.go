// Package dialer places the Studio's calls and follows them to their end. It
// is the one place a number is dialled from, so it is where the calling rules
// hold: the channel limit, the hours, the stop list, the attempts, the
// portions and the guard between them.
package dialer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Islomov49/gapi-voice-studio/server/internal/analyze"
	"github.com/Islomov49/gapi-voice-studio/server/internal/compile"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
	"github.com/Islomov49/gapi-voice-studio/server/internal/rules"
	"github.com/Islomov49/gapi-voice-studio/server/internal/seal"
)

type Engine struct {
	DB       *pgxpool.Pool
	Gapi     *gapi.Client
	Seal     *seal.Sealer
	Now      func() time.Time
	Analyzer analyze.Analyzer

	mu sync.Mutex // one Tick at a time
}

// ErrBusy: every channel of the workspace has a call in progress.
var ErrBusy = errors.New("all channels are busy")

// How long a finished call's text is waited for before the call is closed
// without it, and how long a call may stay unanswered by Gapi's own account.
const (
	transcriptWait = 60 * time.Second
	ringLimit      = 3 * time.Minute
)

// reserve takes one of the workspace's channels for a new call and writes the
// call's row; the count and the insert happen under one lock per workspace so
// that two starts cannot both take the last channel.
func (e *Engine) reserve(ctx context.Context, workspaceID string, channels int, kind string, campaignID, targetID *string, versionID, number string) (string, error) {
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, workspaceID); err != nil {
		return "", err
	}
	var open int
	if err := tx.QueryRow(ctx, `select count(*) from calls where workspace_id = $1 and settled_at is null`, workspaceID).Scan(&open); err != nil {
		return "", err
	}
	if open >= channels {
		return "", ErrBusy
	}
	var id string
	err = tx.QueryRow(ctx, `insert into calls (workspace_id, kind, campaign_id, target_id, agent_version_id, phone, created_at)
		values ($1, $2, $3, $4, $5, $6, $7) returning id`, workspaceID, kind, campaignID, targetID, versionID, number, e.Now()).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// StartTest calls the tester with one version of an agent. The call's row is
// answered at once; Tick follows the call from there.
func (e *Engine) StartTest(ctx context.Context, workspaceID, token string, channels int, versionID, agentName string, spec model.Spec, number string, values map[string]string, line string) (string, error) {
	agent, err := compile.Build(agentName, spec, compile.DefaultLimits)
	if err != nil {
		return "", err
	}
	filled, err := compile.Values(spec, values)
	if err != nil {
		return "", err
	}
	id, err := e.reserve(ctx, workspaceID, channels, "test", nil, nil, versionID, number)
	if err != nil {
		return "", err
	}
	call, err := e.Gapi.CallPhone(ctx, token, agent, number, line, filled)
	if err != nil {
		_, _ = e.DB.Exec(context.WithoutCancel(ctx), `delete from calls where id = $1`, id)
		return "", err
	}
	_, err = e.DB.Exec(ctx, `update calls set gapi_call_id = $2 where id = $1`, id, call.ID)
	return id, err
}

// Run ticks until the context ends.
func (e *Engine) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Tick(ctx)
		}
	}
}

// Tick does one round: close the calls that ended, move campaigns on to
// their next portion or stop them, and dial what is due.
func (e *Engine) Tick(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	tokens := map[string]string{}
	steps := []struct {
		name string
		run  func(context.Context, map[string]string) error
	}{{"settle", e.settle}, {"advance", e.advance}, {"dial", e.dial}}
	for _, step := range steps {
		if err := step.run(ctx, tokens); err != nil && ctx.Err() == nil {
			slog.Error("dialer: "+step.name, "err", err)
		}
	}
}

func (e *Engine) token(tokens map[string]string, workspaceID string, sealed []byte) (string, bool) {
	if t, ok := tokens[workspaceID]; ok {
		return t, t != ""
	}
	t, err := e.Seal.Open(sealed)
	if err != nil {
		slog.Error("dialer: a workspace's token cannot be read", "workspace", workspaceID)
		t = ""
	}
	tokens[workspaceID] = t
	return t, t != ""
}

type openCall struct {
	id, workspaceID, kind, gapiID, status string
	targetID                              *string
	createdAt                             time.Time
	startedAt                             *time.Time
	sealed                                []byte
	spec                                  model.Spec
}

func (e *Engine) settle(ctx context.Context, tokens map[string]string) error {
	rows, err := e.DB.Query(ctx, `select c.id, c.workspace_id, c.kind, c.gapi_call_id, c.status, c.target_id, c.created_at, c.started_at, w.gapi_token_sealed, v.spec
		from calls c join workspaces w on w.id = c.workspace_id join agent_versions v on v.id = c.agent_version_id
		where c.settled_at is null order by c.created_at limit 300`)
	if err != nil {
		return err
	}
	calls, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (openCall, error) {
		c := openCall{}
		return c, row.Scan(&c.id, &c.workspaceID, &c.kind, &c.gapiID, &c.status, &c.targetID, &c.createdAt, &c.startedAt, &c.sealed, &c.spec)
	})
	if err != nil {
		return err
	}
	now := e.Now()
	for _, c := range calls {
		token, ok := e.token(tokens, c.workspaceID, c.sealed)
		if !ok {
			continue
		}
		if c.gapiID == "" {
			// The row was written and Gapi never asked: the service stopped in between.
			if now.Sub(c.createdAt) > time.Minute {
				if err := e.finish(ctx, c, &gapi.Call{Status: "failed", EndReason: "not_started"}, nil, now); err != nil {
					slog.Error("dialer: closing a call", "call", c.id, "err", err)
				}
			}
			continue
		}
		g, err := e.Gapi.Get(ctx, token, c.gapiID)
		if err != nil {
			if code := gapi.Code(err); code == "voice_call_not_found" || code == "not_found" {
				_ = e.finish(ctx, c, &gapi.Call{Status: "failed", EndReason: "lost"}, nil, now)
			}
			continue
		}
		switch g.Status {
		case "created":
			if now.Sub(c.createdAt) > ringLimit {
				_ = e.Gapi.End(ctx, token, c.gapiID)
			}
		case "active":
			if c.status != "active" {
				_, _ = e.DB.Exec(ctx, `update calls set status = 'active', started_at = $2 where id = $1`, c.id, g.StartedAt)
			}
		case "ended", "failed":
			ended := now
			if g.EndedAt != nil {
				ended = *g.EndedAt
			}
			var text *gapi.Transcript
			if g.Transcript {
				if text, err = e.Gapi.Transcript(ctx, token, c.gapiID); err != nil {
					text = nil
				}
			}
			// The text arrives a few seconds after the end; a call that was
			// answered waits for it, since the checks and the form read it.
			if g.Status == "ended" && text == nil && now.Sub(ended) < transcriptWait {
				if c.status != "ended" {
					_, _ = e.DB.Exec(ctx, `update calls set status = 'ended', started_at = $2, ended_at = $3 where id = $1`, c.id, g.StartedAt, ended)
				}
				continue
			}
			if err := e.finish(ctx, c, g, text, now); err != nil {
				slog.Error("dialer: closing a call", "call", c.id, "err", err)
			}
		}
	}
	return nil
}

// finish writes a call's end and moves its person on.
func (e *Engine) finish(ctx context.Context, c openCall, g *gapi.Call, text *gapi.Transcript, now time.Time) error {
	res := &analyze.Result{}
	checks := map[string]any{}
	analysis := "none"
	if text != nil {
		var err error
		if res, err = e.Analyzer.Analyze(ctx, c.spec, text, now); err != nil {
			slog.Warn("dialer: reading a call's text failed, the plain checks stand in", "call", c.id, "err", err)
			res, _ = analyze.Baseline{}.Analyze(ctx, c.spec, text, now)
			analysis = "failed"
		} else {
			analysis = "done"
		}
		checks["ai_disclosed"], checks["do_not_call"] = res.Disclosed, res.DoNotCall
	}
	outcome := g.Outcome
	if outcome == "" && text != nil {
		outcome = text.Outcome
	}
	answers := res.Answers
	if answers == nil {
		answers = map[string]model.Answer{}
	}
	var turns any
	if text != nil {
		turns = text.Turns
	}
	ended := g.EndedAt
	if ended == nil {
		ended = &now
	}
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var number string
	err = tx.QueryRow(ctx, `update calls set status = $2, end_reason = $3, outcome = $4, duration_seconds = $5, charged_g = $6, started_at = $7, ended_at = $8,
		transcript = $9, answers = $10, summary = $11, checks = $12, analysis = $13, settled_at = $14 where id = $1 and settled_at is null returning phone`,
		c.id, g.Status, g.EndReason, outcome, g.DurationSeconds, g.ChargedG, g.StartedAt, ended, turns, answers, res.Summary, checks, analysis, now).Scan(&number)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // closed by another round
	}
	if err != nil {
		return err
	}
	if res.DoNotCall {
		if _, err := tx.Exec(ctx, `insert into stoplist (workspace_id, phone, reason, source, call_id, created_at) values ($1, $2, 'asked not to be called', 'call', $3, $4)
			on conflict do nothing`, c.workspaceID, number, c.id, now); err != nil {
			return err
		}
	}
	if c.targetID != nil {
		var attempts int
		var r model.Rules
		if err := tx.QueryRow(ctx, `select t.attempts, c.rules from targets t join campaigns c on c.id = t.campaign_id where t.id = $1 for update of t`, *c.targetID).Scan(&attempts, &r); err != nil {
			return err
		}
		d := rules.AfterCall(r, attempts, rules.Ended{Status: g.Status, EndReason: g.EndReason, Outcome: outcome, CallbackAt: res.CallbackAt, DoNotCall: res.DoNotCall}, now)
		var next *time.Time
		if d.Status == "pending" {
			next = &d.NextAt
		}
		if _, err := tx.Exec(ctx, `update targets set status = $2, next_at = $3, result = $4 where id = $1`, *c.targetID, d.Status, next, d.Result); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// advance hands a running campaign its next portion once the current one has
// had its first calls, stops it if the portion looked wrong, and closes it
// when nobody is left.
func (e *Engine) advance(ctx context.Context, _ map[string]string) error {
	rows, err := e.DB.Query(ctx, `select id, portion, guard_ok, rules from campaigns where status = 'running'`)
	if err != nil {
		return err
	}
	type campaign struct {
		id               string
		portion, guarded int
		rules            model.Rules
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (campaign, error) {
		c := campaign{}
		return c, row.Scan(&c.id, &c.portion, &c.guarded, &c.rules)
	})
	if err != nil {
		return err
	}
	for _, c := range list {
		var busy bool
		if err := e.DB.QueryRow(ctx, `select exists (select 1 from targets where campaign_id = $1 and portion = $2
			and (status = 'calling' or (status = 'pending' and attempts = 0)))`, c.id, c.portion).Scan(&busy); err != nil {
			return err
		}
		if busy {
			continue
		}
		if c.guarded < c.portion {
			var p rules.Portion
			if err := e.DB.QueryRow(ctx, `select count(*),
					count(*) filter (where k.status = 'failed' and k.end_reason not in ('busy', 'no_answer', 'declined', 'wrong_number')),
					count(*) filter (where k.status = 'ended'),
					count(*) filter (where k.checks->>'do_not_call' = 'true'),
					count(*) filter (where k.status = 'ended' and k.checks->>'ai_disclosed' = 'false')
				from calls k join targets t on t.id = k.target_id where t.campaign_id = $1 and t.portion = $2`, c.id, c.portion).
				Scan(&p.Calls, &p.Technical, &p.Talked, &p.DoNotCall, &p.Undisclosed); err != nil {
				return err
			}
			if reason := rules.Guard(p); reason != "" {
				if _, err := e.DB.Exec(ctx, `update campaigns set status = 'paused', pause_reason = $2 where id = $1 and status = 'running'`, c.id, reason); err != nil {
					return err
				}
				continue
			}
		}
		tag, err := e.DB.Exec(ctx, `update targets set portion = $2 where id in
			(select id from targets where campaign_id = $1 and portion = 0 and status = 'pending' order by row_no limit $3)`, c.id, c.portion+1, c.rules.Portion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if _, err := e.DB.Exec(ctx, `update campaigns set portion = $2 where id = $1`, c.id, c.portion+1); err != nil {
				return err
			}
			continue
		}
		// Nobody new to hand out: the campaign is over once the people waiting
		// for another attempt have had it.
		if _, err := e.DB.Exec(ctx, `update campaigns set status = 'done', finished_at = $2 where id = $1 and status = 'running'
			and not exists (select 1 from targets where campaign_id = $1 and status in ('pending', 'calling'))`, c.id, e.Now()); err != nil {
			return err
		}
	}
	return nil
}

type running struct {
	id, workspaceID, line, versionID, agentName string
	portion, channels                           int
	rules                                       model.Rules
	sealed                                      []byte
	spec                                        model.Spec
}

func (e *Engine) dial(ctx context.Context, tokens map[string]string) error {
	rows, err := e.DB.Query(ctx, `select c.id, c.workspace_id, c.line, c.agent_version_id, a.name, c.portion, w.channels, c.rules, w.gapi_token_sealed, v.spec
		from campaigns c join workspaces w on w.id = c.workspace_id join agent_versions v on v.id = c.agent_version_id join agents a on a.id = v.agent_id
		where c.status = 'running' order by c.started_at`)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (running, error) {
		c := running{}
		return c, row.Scan(&c.id, &c.workspaceID, &c.line, &c.versionID, &c.agentName, &c.portion, &c.channels, &c.rules, &c.sealed, &c.spec)
	})
	if err != nil {
		return err
	}
	now := e.Now()
	full := map[string]bool{} // workspaces with no channel left, or that Gapi has no room for right now
	for _, c := range list {
		if full[c.workspaceID] || !rules.Open(c.rules, now) {
			continue
		}
		token, ok := e.token(tokens, c.workspaceID, c.sealed)
		if !ok {
			continue
		}
		for !full[c.workspaceID] {
			more, err := e.dialOne(ctx, c, token, now)
			if err != nil {
				slog.Error("dialer: a call", "campaign", c.id, "err", err)
				break
			}
			if more == stop {
				break
			}
			if more == wait {
				full[c.workspaceID] = true
			}
		}
	}
	return nil
}

type next int

const (
	again next = iota // look for the campaign's next person
	stop              // nobody is due, or the campaign has stopped
	wait              // the workspace cannot take another call this round
)

// dialOne calls the campaign's next person who is due.
func (e *Engine) dialOne(ctx context.Context, c running, token string, now time.Time) (next, error) {
	var targetID, number string
	var vars map[string]string
	err := e.DB.QueryRow(ctx, `update targets set status = 'calling', attempts = attempts + 1 where id =
		(select id from targets where campaign_id = $1 and status = 'pending' and portion between 1 and $2 and (next_at is null or next_at <= $3)
		 order by next_at nulls first, row_no limit 1 for update skip locked) returning id, phone, vars`, c.id, c.portion, now).Scan(&targetID, &number, &vars)
	if errors.Is(err, pgx.ErrNoRows) {
		return stop, nil
	}
	if err != nil {
		return stop, err
	}
	// back puts the person where they were: this round did not call them.
	back := func(after time.Duration) {
		var at *time.Time
		if after > 0 {
			t := now.Add(after)
			at = &t
		}
		_, _ = e.DB.Exec(context.WithoutCancel(ctx), `update targets set status = 'pending', attempts = attempts - 1, next_at = coalesce($2, next_at) where id = $1`, targetID, at)
	}
	pause := func(reason string) {
		_, _ = e.DB.Exec(context.WithoutCancel(ctx), `update campaigns set status = 'paused', pause_reason = $2 where id = $1 and status = 'running'`, c.id, reason)
	}
	var stopped bool
	if err := e.DB.QueryRow(ctx, `select exists (select 1 from stoplist where workspace_id = $1 and phone = $2)`, c.workspaceID, number).Scan(&stopped); err != nil {
		back(0)
		return stop, err
	}
	if stopped {
		_, err := e.DB.Exec(ctx, `update targets set status = 'skipped', attempts = attempts - 1, result = 'stop_list' where id = $1`, targetID)
		return again, err
	}
	agent, err := compile.Build(c.agentName, c.spec, compile.DefaultLimits)
	if err != nil {
		back(0)
		pause("agent_invalid")
		return stop, nil
	}
	values, err := compile.Values(c.spec, vars)
	if err != nil {
		_, err := e.DB.Exec(ctx, `update targets set status = 'failed', result = 'invalid_data' where id = $1`, targetID)
		return again, err
	}
	callID, err := e.reserve(ctx, c.workspaceID, c.channels, "campaign", &c.id, &targetID, c.versionID, number)
	if errors.Is(err, ErrBusy) {
		back(0)
		return wait, nil
	}
	if err != nil {
		back(0)
		return stop, err
	}
	undo := func() { _, _ = e.DB.Exec(context.WithoutCancel(ctx), `delete from calls where id = $1`, callID) }
	g, err := e.Gapi.CallPhone(ctx, token, agent, number, c.line, values)
	if err == nil {
		_, err = e.DB.Exec(ctx, `update calls set gapi_call_id = $2 where id = $1`, callID, g.ID)
		if err == nil {
			_, err = e.DB.Exec(ctx, `update targets set last_call_id = $2 where id = $1`, targetID, callID)
		}
		return again, err
	}
	switch code := gapi.Code(err); code {
	case "voice_call_invalid":
		// Gapi does not take the number: the person cannot be called.
		undo()
		_, err := e.DB.Exec(ctx, `update targets set status = 'failed', result = 'wrong_number' where id = $1`, targetID)
		return again, err
	case "quota_exceeded":
		undo()
		back(0)
		pause("balance")
		return stop, nil
	case "voice_line_not_found":
		undo()
		back(0)
		pause("line")
		return stop, nil
	case "voice_not_available", "unauthorized", "invalid_token", "forbidden":
		undo()
		back(0)
		pause("access")
		return stop, nil
	case "voice_agent_invalid":
		undo()
		back(0)
		pause("agent_invalid")
		return stop, nil
	default:
		// No room on the runners, the call server, the network: try again shortly.
		undo()
		back(30 * time.Second)
		return wait, nil
	}
}

// Portion gives a campaign its first portion: called when it starts.
func FirstPortion(ctx context.Context, tx pgx.Tx, campaignID string, r model.Rules) error {
	_, err := tx.Exec(ctx, `update targets set portion = 1 where id in
		(select id from targets where campaign_id = $1 and portion = 0 and status = 'pending' order by row_no limit $2)`, campaignID, r.FirstPortion)
	return err
}

// Raw is a jsonb value read as it is.
type Raw = json.RawMessage
