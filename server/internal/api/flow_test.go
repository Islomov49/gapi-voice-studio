package api

// The whole road of the PRD against a real Postgres and a stand-in for Gapi:
// a project is connected, an agent made from a template, a list uploaded, a
// campaign refused until a test call is accepted, then called in portions
// under the rules (channels, hours, stop list, attempts) to its end.
//
//	docker run -d --name studio-test-pg -e POSTGRES_PASSWORD=test -p 55440:5432 postgres:16
//	STUDIO_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55440/postgres go test ./...

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"github.com/Islomov49/gapi-voice-studio/server/internal/analyze"
	"github.com/Islomov49/gapi-voice-studio/server/internal/crm/crmtest"
	"github.com/Islomov49/gapi-voice-studio/server/internal/db"
	"github.com/Islomov49/gapi-voice-studio/server/internal/dialer"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi/gapitest"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gemini"
	"github.com/Islomov49/gapi-voice-studio/server/internal/rules"
	"github.com/Islomov49/gapi-voice-studio/server/internal/seal"
)

type fixture struct {
	t      *testing.T
	fake   *gapitest.Fake
	model  *fakeGemini
	engine *dialer.Engine
	pool   *pgxpool.Pool
	http   *http.Client
	base   string
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	url := os.Getenv("STUDIO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("STUDIO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	raw, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx, `drop schema public cascade; create schema public`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, fake: gapitest.New(), pool: pool}
	f.now, _ = time.ParseInLocation("2006-01-02 15:04", "2026-11-16 10:00", rules.Tashkent) // a Monday
	clock := func() time.Time { return f.now }
	sealer, _ := seal.New("a secret long enough for a test")
	client := gapi.New(f.fake.URL)
	f.model = newFakeGemini()
	f.engine = &dialer.Engine{DB: pool, Gapi: client, Seal: sealer, Now: clock, Analyzer: analyze.Baseline{}, Gemini: gemini.New(f.model.server.URL)}
	server := httptest.NewServer((&Server{DB: pool, Gapi: client, Seal: sealer, Now: clock, Engine: f.engine}).Handler())
	jar, _ := cookiejar.New(nil)
	f.http, f.base = &http.Client{Jar: jar}, server.URL
	t.Cleanup(func() { server.Close(); f.fake.Close(); f.model.server.Close(); pool.Close() })
	return f
}

// call sends a request and answers its status and its JSON.
func (f *fixture) call(method, path string, body any) (int, map[string]any) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.base+path, rd)
	resp, err := f.http.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// must is call for a request that has to succeed.
func (f *fixture) must(method, path string, body any) map[string]any {
	f.t.Helper()
	status, out := f.call(method, path, body)
	if status >= 300 {
		f.t.Fatalf("%s %s: %d %v", method, path, status, out)
	}
	return out
}

func (f *fixture) refused(method, path string, body any, code string) {
	f.t.Helper()
	if status, out := f.call(method, path, body); status < 400 || out["error"] != code {
		f.t.Fatalf("%s %s: want %s, got %d %v", method, path, code, status, out)
	}
}

func (f *fixture) upload(name, content string) map[string]any {
	f.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", name)
	_, _ = part.Write([]byte(content))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", f.base+"/api/lists", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := f.http.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 201 {
		f.t.Fatalf("upload: %d %v", resp.StatusCode, out)
	}
	return out
}

func (f *fixture) tick() { f.engine.Tick(context.Background()) }

func (f *fixture) at(clock string) {
	f.now, _ = time.ParseInLocation("2006-01-02 15:04", clock, rules.Tashkent)
}

const greeting = "Assalomu alaykum! Men «Namuna Mebel» kompaniyasining sun'iy intellekt yordamchisiman."

func (f *fixture) counts(campaign string) map[string]any {
	return f.must("GET", "/api/campaigns/"+campaign, nil)["campaign"].(map[string]any)["counts"].(map[string]any)
}

func TestFlow(t *testing.T) {
	f := newFixture(t)

	// nothing without a session; a token Gapi accepts opens one
	f.refused("GET", "/api/agents", nil, "signed_out")
	f.refused("POST", "/api/connect", map[string]any{"token": "short"}, "token_invalid")
	f.must("POST", "/api/connect", map[string]any{"token": "gapi_ab12cd34_0123456789abcdef0123456789abcdef"})
	me := f.must("GET", "/api/me", nil)
	if needs, _ := json.Marshal(me["needs"]); string(needs) != `["tester_phone"]` {
		t.Fatalf("needs: %s", needs)
	}

	agent := f.must("POST", "/api/agents", map[string]any{"template": "feedback"})
	version := agent["latest"].(map[string]any)["id"].(string)
	agentID := agent["agent"].(map[string]any)["id"].(string)
	if p := agent["problems"].([]any); len(p) != 0 || !strings.Contains(agent["prompt"].(string), greeting) {
		t.Fatalf("a template's agent: problems %v", p)
	}

	// a test call needs to know whom to call
	f.refused("POST", "/api/calls/test", map[string]any{"version_id": version}, "tester_phone_needed")
	f.must("PUT", "/api/workspace", map[string]any{"tester_phone": "90 000 00 01", "name": "Namuna"})

	list := f.upload("clients.csv", "Имя,Телефон\nAli,90 111 00 01\nVali,90 111 00 02\nGani,90 111 00 03\nSoli,90 111 00 04\nHali,90 111 00 05\nAli again,90 111 00 01\nNo number,\n")
	if stats := list["stats"].(map[string]any); stats["accepted"] != 5.0 || stats["duplicates"] != 1.0 || stats["invalid"] != 1.0 {
		t.Fatalf("upload stats: %v", stats)
	}
	f.must("POST", "/api/stoplist", map[string]any{"phones": []string{"+998901110005"}, "reason": "asked earlier"})

	rulesBody := map[string]any{"from": "09:00", "to": "20:00", "days": []int{1, 2, 3, 4, 5, 6}, "max_attempts": 2, "retry_minutes": 120, "first_portion": 2, "portion": 2}
	f.refused("POST", "/api/campaigns", map[string]any{"name": "Night", "version_id": version, "list_id": list["id"],
		"rules": map[string]any{"from": "06:00", "to": "23:00", "days": []int{1}, "max_attempts": 2, "retry_minutes": 120, "first_portion": 2, "portion": 2}}, "rules_invalid")
	campaign := f.must("POST", "/api/campaigns", map[string]any{"name": "Feedback", "version_id": version, "list_id": list["id"], "rules": rulesBody})["campaign"].(map[string]any)["id"].(string)
	if c := f.counts(campaign); c["pending"] != 4.0 || c["skipped"] != 1.0 {
		t.Fatalf("a draft's people: %v", c)
	}

	// the gate: no campaign before a person accepts a test call of this version
	f.refused("POST", "/api/campaigns/"+campaign+"/start", nil, "test_call_needed")

	// a test call in which the agent does not say it is an AI assistant cannot be accepted
	test1 := f.must("POST", "/api/calls/test", map[string]any{"version_id": version})["call_id"].(string)
	placed := f.fake.Placed()
	if len(placed) != 1 || placed[0].To != "+998900000001" || !strings.Contains(placed[0].Agent.Prompt, greeting) || placed[0].Agent.FirstSpeaker != "caller" {
		t.Fatalf("the test call as Gapi got it: %+v", placed)
	}
	f.refused("POST", "/api/calls/"+test1+"/verdict", map[string]any{"accepted": true}, "call_not_answered")
	f.fake.Talk("g1", "agreed", f.now, "Alo?", ">Alo", "Assalomu alaykum, men Nilufarman.", ">Yaxshi", "Rahmat, xayr.")
	f.tick()
	f.refused("POST", "/api/calls/"+test1+"/verdict", map[string]any{"accepted": true}, "agent_did_not_introduce_itself")
	f.must("POST", "/api/calls/"+test1+"/verdict", map[string]any{"accepted": false, "feedback": "did not say who is calling"})
	f.refused("POST", "/api/campaigns/"+campaign+"/start", nil, "test_call_needed")

	test2 := f.must("POST", "/api/calls/test", map[string]any{"version_id": version, "values": map[string]string{"client_name": "Tester"}})["call_id"].(string)
	f.fake.Talk("g2", "agreed", f.now, "Alo?", ">Alo", greeting, ">Ha, besh", "Rahmat!")
	f.tick()
	call := f.must("GET", "/api/calls/"+test2, nil)
	if call["status"] != "ended" || call["settled"] != true || call["checks"].(map[string]any)["ai_disclosed"] != true || len(call["transcript"].([]any)) != 5 {
		t.Fatalf("the test call after it ended: %v", call)
	}
	f.must("POST", "/api/calls/"+test2+"/verdict", map[string]any{"accepted": true})

	// a change of the script is a new version, which is not accepted
	spec := agent["latest"].(map[string]any)["spec"].(map[string]any)
	spec["script"] = spec["script"].(string) + "\n- Yangi qoida."
	changed := f.must("POST", "/api/agents/"+agentID+"/versions", map[string]any{"spec": spec, "note": "a rule added"})
	if latest := changed["latest"].(map[string]any); latest["n"] != 2.0 || latest["accepted_at"] != nil {
		t.Fatalf("the new version: %v", latest)
	}
	if again := f.must("POST", "/api/agents/"+agentID+"/versions", map[string]any{"spec": spec}); again["latest"].(map[string]any)["n"] != 2.0 {
		t.Fatal("saving the same text made a version")
	}

	// started: the first portion is two people, called with their own names
	if started := f.must("POST", "/api/campaigns/"+campaign+"/start", nil); started["calling_now"] != true {
		t.Fatalf("start: %v", started)
	}
	f.tick()
	placed = f.fake.Placed()
	if len(placed) != 4 || placed[2].To != "+998901110001" || placed[2].Variables["client_name"] != "Ali" || placed[3].To != "+998901110002" {
		t.Fatalf("the first portion: %d calls", len(placed))
	}
	f.tick()
	if len(f.fake.Placed()) != 4 {
		t.Fatal("the second portion started before the first was called")
	}

	// one asks not to be called (on the stop list from then on), one does not answer
	f.fake.Talk("g3", "declined", f.now, "Alo?", ">Alo", greeting, ">Boshqa qo'ng'iroq qilmanglar!", "Uzr, xayr.")
	f.fake.Fail("g4", "no_answer", f.now)
	f.tick()
	stop := f.must("GET", "/api/stoplist", nil)["stoplist"].([]any)
	if len(stop) != 2 || stop[0].(map[string]any)["source"] != "call" {
		t.Fatalf("the stop list: %v", stop)
	}
	// the same round hands out the second portion and calls it
	if placed = f.fake.Placed(); len(placed) != 6 || placed[4].To != "+998901110003" || placed[5].To != "+998901110004" {
		t.Fatalf("the second portion: %d calls", len(placed))
	}

	// every channel busy: a third campaign call would fit, a fourth call does not
	f.must("POST", "/api/calls/test", map[string]any{"version_id": version})
	f.refused("POST", "/api/calls/test", map[string]any{"version_id": version}, "channels_busy")
	f.fake.Fail("g7", "busy", f.now)

	// in the evening nobody is called: the people not reached wait for the morning
	f.at("2026-11-16 21:00")
	f.fake.Fail("g5", "no_answer", f.now)
	f.fake.Fail("g6", "busy", f.now)
	f.tick()
	f.tick()
	if n := len(f.fake.Placed()); n != 7 {
		t.Fatalf("calls placed after hours: %d", n)
	}
	if c := f.counts(campaign); c["pending"] != 3.0 || c["done"] != 1.0 || c["skipped"] != 1.0 {
		t.Fatalf("in the evening: %v", c)
	}

	// the next morning the three are tried once more, three channels at once
	f.at("2026-11-17 09:05")
	f.tick()
	if placed = f.fake.Placed(); len(placed) != 10 {
		t.Fatalf("the morning's retries: %d calls", len(placed))
	}
	for _, id := range []string{"g8", "g9", "g10"} {
		f.fake.Fail(id, "no_answer", f.now)
	}
	f.tick()
	f.tick()
	got := f.must("GET", "/api/campaigns/"+campaign, nil)
	if c := got["campaign"].(map[string]any); c["status"] != "done" || c["counts"].(map[string]any)["failed"] != 3.0 {
		t.Fatalf("the end: %v", c)
	}
	if r := got["results"].(map[string]any); r["do_not_call"] != 1.0 || r["no_answer"] != 3.0 || r["stop_list"] != 1.0 {
		t.Fatalf("results: %v", r)
	}

	// the results as a workbook: a header and a row per person of the list
	resp, err := f.http.Get(f.base + "/api/campaigns/" + campaign + "/export.xlsx")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("export: %v", err)
	}
	defer resp.Body.Close()
	book, err := excelize.OpenReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := book.GetRows(book.GetSheetName(0))
	if len(rows) != 6 || rows[0][1] != "Имя" || rows[1][0] != "+998901110001" || rows[1][4] != "do_not_call" {
		t.Fatalf("the workbook: %v", rows)
	}
}

// A portion in which the agent did not introduce itself stops the campaign;
// a person who has looked lets it go on.
func TestGuardPauses(t *testing.T) {
	f := newFixture(t)
	f.must("POST", "/api/connect", map[string]any{"token": "gapi_ab12cd34_0123456789abcdef0123456789abcdef"})
	f.must("PUT", "/api/workspace", map[string]any{"tester_phone": "+998900000001"})
	version := f.must("POST", "/api/agents", map[string]any{"template": "feedback"})["latest"].(map[string]any)["id"].(string)
	test := f.must("POST", "/api/calls/test", map[string]any{"version_id": version})["call_id"].(string)
	f.fake.Talk("g1", "agreed", f.now, "Alo?", ">Alo", greeting)
	f.tick()
	f.must("POST", "/api/calls/"+test+"/verdict", map[string]any{"accepted": true})
	list := f.upload("l.csv", "phone\n901110001\n901110002\n901110003\n")
	campaign := f.must("POST", "/api/campaigns", map[string]any{"name": "c", "version_id": version, "list_id": list["id"],
		"rules": map[string]any{"from": "09:00", "to": "20:00", "days": []int{1}, "max_attempts": 1, "retry_minutes": 60, "first_portion": 2, "portion": 2}})["campaign"].(map[string]any)["id"].(string)
	f.must("POST", "/api/campaigns/"+campaign+"/start", nil)

	// Gapi has no room: the people stay in the queue and are called a little later
	f.fake.RefuseCode, f.fake.RefuseStatus = "voice_no_capacity", 503
	f.tick()
	if n := len(f.fake.Placed()); n != 1 {
		t.Fatalf("calls placed while Gapi was full: %d", n)
	}
	f.now = f.now.Add(time.Minute)
	f.tick()
	if n := len(f.fake.Placed()); n != 3 {
		t.Fatalf("calls placed once Gapi had room: %d", n)
	}

	f.fake.Talk("g2", "agreed", f.now, "Alo?", ">Alo", "Salom, men Nilufarman.", ">Ha")
	f.fake.Talk("g3", "agreed", f.now, "Alo?", ">Alo", "Salom, men Nilufarman.", ">Yo'q")
	f.tick()
	c := f.must("GET", "/api/campaigns/"+campaign, nil)["campaign"].(map[string]any)
	if c["status"] != "paused" || c["pause_reason"] != "agent_did_not_introduce_itself" || len(f.fake.Placed()) != 3 {
		t.Fatalf("after a portion without the greeting: %v", c)
	}
	f.must("POST", "/api/campaigns/"+campaign+"/start", nil)
	f.tick()
	if n := len(f.fake.Placed()); n != 4 {
		t.Fatalf("after a person let it go on: %d calls", n)
	}

	// the balance runs out: the campaign stops and says why, the person keeps their place
	f.fake.Fail("g4", "no_answer", f.now)
	list2 := f.upload("l2.csv", "phone\n901110009\n")
	second := f.must("POST", "/api/campaigns", map[string]any{"name": "c2", "version_id": version, "list_id": list2["id"]})["campaign"].(map[string]any)["id"].(string)
	f.must("POST", "/api/campaigns/"+second+"/start", nil)
	f.fake.RefuseCode, f.fake.RefuseStatus = "quota_exceeded", 402
	f.tick()
	c = f.must("GET", "/api/campaigns/"+second, nil)["campaign"].(map[string]any)
	if c["status"] != "paused" || c["pause_reason"] != "balance" || c["counts"].(map[string]any)["pending"] != 1.0 {
		t.Fatalf("out of balance: %v", c)
	}
}

// A person who asks to be called later is called at the time the agent
// repeated, past the campaign's attempts; the model's "callback" label alone
// (a manager will call) does not make the agent call again.
func TestCallLater(t *testing.T) {
	f := newFixture(t)
	f.must("POST", "/api/connect", map[string]any{"token": "gapi_ab12cd34_0123456789abcdef0123456789abcdef"})
	f.must("PUT", "/api/workspace", map[string]any{"tester_phone": "+998900000001"})
	version := f.must("POST", "/api/agents", map[string]any{"template": "feedback"})["latest"].(map[string]any)["id"].(string)
	test := f.must("POST", "/api/calls/test", map[string]any{"version_id": version})["call_id"].(string)
	f.fake.Talk("g1", "callback", f.now, "Alo?", ">Alo", greeting, ">Hozir bandman, yarim soatdan keyin", "Xo'p, sizga yarim soatdan keyin qayta qo'ng'iroq qilamiz. Xayr!")
	f.tick()
	checks := f.must("GET", "/api/calls/"+test, nil)["checks"].(map[string]any)
	if checks["callback"] != true || checks["callback_at"] != f.now.Add(30*time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("the test call's promise to call again: %v", checks)
	}
	f.must("POST", "/api/calls/"+test+"/verdict", map[string]any{"accepted": true})
	list := f.upload("l.csv", "phone\n901110001\n901110002\n")
	campaign := f.must("POST", "/api/campaigns", map[string]any{"name": "c", "version_id": version, "list_id": list["id"],
		"rules": map[string]any{"from": "09:00", "to": "20:00", "days": []int{1}, "max_attempts": 1, "retry_minutes": 60, "first_portion": 2, "portion": 2}})["campaign"].(map[string]any)["id"].(string)
	f.must("POST", "/api/campaigns/"+campaign+"/start", nil)
	f.tick()
	f.fake.Talk("g2", "callback", f.now, "Alo?", ">Alo", greeting, ">O'n daqiqadan keyin qo'ng'iroq qiling", "Xo'p, sizga o'n daqiqadan keyin qayta qo'ng'iroq qilamiz. Xayr!")
	f.fake.Talk("g3", "callback", f.now, "Alo?", ">Alo", greeting, ">Ikki", "Afsus. Nima yoqmadi?", ">Kechikdi", "Uzr so'raymiz. Rahbarimiz bugun sizga qo'ng'iroq qiladi.")
	f.tick()
	rows := f.must("GET", "/api/campaigns/"+campaign+"/targets", nil)["targets"].([]any)
	first, second := rows[0].(map[string]any), rows[1].(map[string]any)
	if first["status"] != "pending" || first["result"] != "call_later" || second["status"] != "done" || second["result"] != "callback" {
		t.Fatalf("after the two talks: %v / %v", first, second)
	}
	if c := f.must("GET", "/api/campaigns/"+campaign, nil)["campaign"].(map[string]any); c["status"] != "running" {
		t.Fatalf("a campaign with a call still to place: %v", c["status"])
	}
	f.now = f.now.Add(9 * time.Minute)
	f.tick()
	if n := len(f.fake.Placed()); n != 3 {
		t.Fatalf("called before the time the person named: %d calls", n)
	}
	f.now = f.now.Add(time.Minute)
	f.tick()
	if n := len(f.fake.Placed()); n != 4 {
		t.Fatalf("not called at the time the person named: %d calls", n)
	}
	f.fake.Talk("g4", "resolved", f.now, "Alo?", ">Alo", greeting, ">Besh", "Rahmat! Xayr.")
	f.tick()
	f.tick()
	c := f.must("GET", "/api/campaigns/"+campaign, nil)["campaign"].(map[string]any)
	if c["status"] != "done" || c["counts"].(map[string]any)["done"] != 2.0 {
		t.Fatalf("after the call back: %v", c)
	}
}

// An assistant runs the same road over MCP, and meets the same refusals.
func TestMCP(t *testing.T) {
	f := newFixture(t)
	const token = "gapi_ab12cd34_0123456789abcdef0123456789abcdef"
	id := 0
	rpc := func(method string, params any) map[string]any {
		t.Helper()
		id++
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		req, _ := http.NewRequest("POST", f.base+"/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", method, err, resp)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	// tool calls a tool and answers its JSON and whether it was refused
	tool := func(name string, args map[string]any) (map[string]any, bool) {
		t.Helper()
		res, ok := rpc("tools/call", map[string]any{"name": name, "arguments": args})["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no result", name)
		}
		out := map[string]any{}
		_ = json.Unmarshal([]byte(res["content"].([]any)[0].(map[string]any)["text"].(string)), &out)
		return out, res["isError"].(bool)
	}
	must := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out, refused := tool(name, args)
		if refused {
			t.Fatalf("%s: %v", name, out)
		}
		return out
	}

	// no token, no project
	if resp, _ := http.Post(f.base+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); resp.StatusCode != 401 {
		t.Fatalf("without a token: %d", resp.StatusCode)
	}
	if init := rpc("initialize", map[string]any{"protocolVersion": "2025-03-26"})["result"].(map[string]any); init["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize: %v", init)
	}
	if tools := rpc("tools/list", nil)["result"].(map[string]any)["tools"].([]any); len(tools) != len(mcpTools) || len(tools) < 15 {
		t.Fatalf("tools: %d", len(tools))
	}
	// a notification is taken without an answer
	note, _ := http.NewRequest("POST", f.base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	note.Header.Set("Authorization", "Bearer "+token)
	if resp, _ := http.DefaultClient.Do(note); resp.StatusCode != 202 {
		t.Fatalf("a notification: %d", resp.StatusCode)
	}

	must("set_tester_phone", map[string]any{"phone": "90 000 00 01"})
	agent := must("create_agent", map[string]any{"name": "Feedback", "template": "feedback"})
	version := agent["latest"].(map[string]any)["id"].(string)
	list := must("import_contacts", map[string]any{"filename": "c.csv", "text": "Ism,Telefon\nAli,901110001\nVali,901110002\n"})
	campaign := must("create_campaign", map[string]any{"name": "c", "version_id": version, "list_id": list["id"]})["campaign"].(map[string]any)["id"].(string)
	if out, refused := tool("start_campaign", map[string]any{"campaign_id": campaign}); !refused || out["error"] != "test_call_needed" {
		t.Fatalf("a campaign before the test call: %v", out)
	}

	call := must("start_test_call", map[string]any{"version_id": version})["call_id"].(string)
	f.fake.Talk("g1", "resolved", f.now, "Alo?", ">Alo", greeting, ">Ha, besh qo'yaman, tez yetkazishdi.", "Rahmat!")
	f.tick()
	got := must("get_call", map[string]any{"call_id": call})
	if got["settled"] != true || got["phone"] != "+99890*****01" || got["checks"].(map[string]any)["ai_disclosed"] != true {
		t.Fatalf("the call as the assistant sees it: %v", got)
	}
	must("record_tester_verdict", map[string]any{"call_id": call, "accepted": true, "tester_words": "yaxshi gapirdi"})

	// the form: an answer needs the call's own words behind it
	if out, refused := tool("fill_call_form", map[string]any{"call_id": call, "answers": map[string]any{"rating": map[string]any{"value": 5, "quote": "juda zo'r, besh"}}}); !refused || out["error"] != "quote_not_in_call" {
		t.Fatalf("an answer nobody said: %v", out)
	}
	if out, refused := tool("fill_call_form", map[string]any{"call_id": call, "answers": map[string]any{"rating": map[string]any{"value": 7, "quote": "besh qo'yaman"}}}); !refused || out["error"] != "answer_invalid" {
		t.Fatalf("a rating of seven: %v", out)
	}
	filled := must("fill_call_form", map[string]any{"call_id": call, "summary": "Доволен, оценка 5.",
		"answers": map[string]any{"rating": map[string]any{"value": 5, "quote": "Besh qo‘yaman"}, "liked": map[string]any{"value": "быстрая доставка", "quote": "tez yetkazishdi"}}})
	if a := filled["answers"].(map[string]any); len(a) != 2 || a["rating"].(map[string]any)["value"] != 5.0 {
		t.Fatalf("the filled form: %v", filled)
	}

	must("start_campaign", map[string]any{"campaign_id": campaign})
	f.tick()
	if n := len(f.fake.Placed()); n != 3 {
		t.Fatalf("calls after the campaign started: %d", n)
	}
	rows := must("campaign_results", map[string]any{"campaign_id": campaign})["targets"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["phone"] != "+99890*****01" || rows[0].(map[string]any)["status"] != "calling" {
		t.Fatalf("results: %v", rows)
	}
	f.fake.Talk("g2", "resolved", f.now, "Alo?", ">Alo", greeting, ">To'rt", "Rahmat!")
	f.tick()
	if open := must("list_calls", map[string]any{"needs_form": true})["calls"].([]any); len(open) != 1 {
		t.Fatalf("calls waiting for their form: %d", len(open))
	}
}

// A campaign over the cards of a CRM stage: the people come from the stage,
// each result is written onto its card, and cards that arrive later join.
func TestCRM(t *testing.T) {
	f := newFixture(t)
	portal := crmtest.NewBitrix()
	defer portal.Close()
	portal.Deals = []crmtest.Deal{{ID: "7", Title: "Стол", Stage: "NEW", Contact: "70", Assigned: "5"}, {ID: "8", Title: "Без телефона", Stage: "NEW", Contact: "0"}}
	portal.Contacts["70"] = [2]string{"Ali", "+998 90 111 00 01"}
	portal.Contacts["71"] = [2]string{"Vali", "90 111 00 02"}

	f.must("POST", "/api/connect", map[string]any{"token": "gapi_ab12cd34_0123456789abcdef0123456789abcdef"})
	f.must("PUT", "/api/workspace", map[string]any{"tester_phone": "+998900000001"})
	version := f.must("POST", "/api/agents", map[string]any{"template": "feedback"})["latest"].(map[string]any)["id"].(string)
	test := f.must("POST", "/api/calls/test", map[string]any{"version_id": version})["call_id"].(string)
	f.fake.Talk("g1", "agreed", f.now, "Alo?", ">Alo", greeting)
	f.tick()
	f.must("POST", "/api/calls/"+test+"/verdict", map[string]any{"accepted": true})

	// a webhook that is not one is refused; the stand-in portal speaks http, so its row is written directly
	f.refused("POST", "/api/crm", map[string]any{"kind": "bitrix24", "webhook": "https://example.bitrix24.kz/crm/"}, "crm_invalid")
	sealer, _ := seal.New("a secret long enough for a test")
	var connection string
	if err := f.pool.QueryRow(context.Background(), `insert into crm_connections (workspace_id, kind, name, base, secret_sealed)
		select id, 'bitrix24', 'portal', $1, $2 from workspaces returning id`, portal.URL, sealer.Seal(portal.Secret())).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	if p := f.must("GET", "/api/crm/"+connection+"/pipelines", nil)["pipelines"].([]any); len(p) != 3 {
		t.Fatalf("pipelines: %v", p)
	}
	list := f.must("POST", "/api/crm/"+connection+"/import", map[string]any{"entity": "deal", "pipeline_id": "0", "stage_id": "NEW", "name": "Новые сделки"})
	if stats := list["stats"].(map[string]any); stats["accepted"] != 1.0 || stats["invalid"] != 1.0 {
		t.Fatalf("the stage as a list: %v", list)
	}

	crmRules := map[string]any{"connection_id": connection, "note": true,
		"move": map[string]any{"agreed": map[string]any{"pipeline_id": "0", "stage_id": "WON"}, "*": map[string]any{"pipeline_id": "0", "stage_id": "LOSE"}},
		"task": map[string]any{"agreed": "Клиент согласен: перезвонить и оформить"},
		"feed": map[string]any{"entity": "deal", "pipeline_id": "0", "stage_id": "NEW"}}
	f.refused("POST", "/api/campaigns", map[string]any{"name": "c", "version_id": version, "list_id": list["id"], "crm": map[string]any{"connection_id": "00000000-0000-0000-0000-000000000000"}}, "crm_invalid")
	campaign := f.must("POST", "/api/campaigns", map[string]any{"name": "CRM", "version_id": version, "list_id": list["id"], "crm": crmRules,
		"rules": map[string]any{"from": "09:00", "to": "20:00", "days": []int{1}, "max_attempts": 1, "retry_minutes": 60, "first_portion": 5, "portion": 5}})["campaign"].(map[string]any)["id"].(string)
	f.must("POST", "/api/campaigns/"+campaign+"/start", nil)
	f.tick()
	placed := f.fake.Placed()
	if len(placed) != 2 || placed[1].To != "+998901110001" || placed[1].Variables["client_name"] != "Ali" {
		t.Fatalf("the call to the card's person: %+v", placed[len(placed)-1])
	}

	// the person agrees: a note, the stage and a task for the card's own manager
	f.fake.Talk("g2", "agreed", f.now, "Alo?", ">Alo", greeting, ">Ha, besh", "Rahmat!")
	f.tick()
	log := portal.Log()
	if len(log) != 3 || !strings.HasPrefix(log[0], "comment deal 7: Голосовой агент Gapi «Фидбэк после услуги»\nИтог: согласие") || log[1] != "move deal 7 WON" ||
		log[2] != "todo 2 7 for 5: Клиент согласен: перезвонить и оформить" {
		t.Fatalf("written to the portal: %q", log)
	}

	// the form, filled later, follows as a note of its own
	calls := f.must("GET", "/api/calls?campaign_id="+campaign, nil)["calls"].([]any)
	f.must("PUT", "/api/calls/"+calls[0].(map[string]any)["id"].(string)+"/form", map[string]any{"summary": "Оценка 5.",
		"answers": map[string]any{"rating": map[string]any{"value": 5, "quote": "Ha, besh"}}})
	f.tick()
	if log = portal.Log(); len(log) != 4 || !strings.Contains(log[3], "ответы\nКратко: Оценка 5.\nОценка: 5") {
		t.Fatalf("the form's note: %q", log)
	}

	// a card arrives at the stage: within a minute or two its person is called; nobody answers, and the card goes to the other stage
	portal.Add(crmtest.Deal{ID: "10", Title: "Шкаф", Stage: "NEW", Contact: "71", Assigned: "6"})
	f.now = f.now.Add(2 * time.Minute)
	f.tick()
	if placed = f.fake.Placed(); len(placed) != 3 || placed[2].To != "+998901110002" {
		t.Fatalf("calls after a card arrived: %d", len(placed))
	}
	f.fake.Fail("g3", "no_answer", f.now)
	f.tick()
	if log = portal.Log(); len(log) != 6 || !strings.Contains(log[4], "comment deal 10") || !strings.Contains(log[4], "не ответил") || log[5] != "move deal 10 LOSE" {
		t.Fatalf("a person not reached: %q", log)
	}
	// a campaign fed from a stage does not finish by itself
	if c := f.must("GET", "/api/campaigns/"+campaign, nil)["campaign"].(map[string]any); c["status"] != "running" || c["crm_failed"] != 0.0 {
		t.Fatalf("the campaign: %v", c)
	}
}

// fakeGemini stands in for Google's API: one key it takes, and the answer it
// gives to every call it is asked to read.
type fakeGemini struct {
	server *httptest.Server
	answer map[string]any
	down   bool
	asked  []map[string]any
}

const geminiKey = "AIzaTestKey0123456789abcdefghij"

func newFakeGemini() *fakeGemini {
	g := &fakeGemini{}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Header.Get("x-goog-api-key") != geminiKey:
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error": {"code": "authentication", "message": "The API key is missing, invalid, or expired."}}`))
		case r.Method == "GET" && r.URL.Path == "/v1beta/models":
			_, _ = w.Write([]byte(`{"models": [{"name": "models/gemini-flash-latest"}, {"name": "models/gemini-3.8-flash"}]}`))
		case r.Method == "POST" && r.URL.Path == "/v1beta/interactions" && g.down:
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error": {"code": "rate_limit_exceeded", "message": "slow down"}}`))
		case r.Method == "POST" && r.URL.Path == "/v1beta/interactions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			g.asked = append(g.asked, body)
			text, _ := json.Marshal(g.answer)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "steps": []any{
				map[string]any{"type": "thought"},
				map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": string(text)}}}}})
		default:
			w.WriteHeader(404)
		}
	}))
	return g
}

// With the person's own Gemini key the Studio reads each answered call: the
// form is filled, an answer nobody said is thrown out, a person's correction
// outlives a second reading, and a model that does not answer costs nothing
// but the reading.
func TestAI(t *testing.T) {
	f := newFixture(t)
	f.must("POST", "/api/connect", map[string]any{"token": "gapi_ab12cd34_0123456789abcdef0123456789abcdef"})
	f.must("PUT", "/api/workspace", map[string]any{"tester_phone": "+998900000001"})
	version := f.must("POST", "/api/agents", map[string]any{"template": "feedback"})["latest"].(map[string]any)["id"].(string)

	f.refused("PUT", "/api/ai", map[string]any{"key": "AIzaSomebodyElsesKey0123456789"}, "ai_key_invalid")
	f.refused("PUT", "/api/ai", map[string]any{"key": geminiKey, "model": "gemini-9-ultra"}, "ai_model_unknown")
	if got := f.must("PUT", "/api/ai", map[string]any{"key": geminiKey}); got["connected"] != true || got["model"] != "gemini-flash-latest" {
		t.Fatalf("the key was not kept: %v", got)
	}
	if me := f.must("GET", "/api/me", nil); me["ai"] != true {
		t.Fatalf("me does not say a model reads the calls: %v", me["ai"])
	}

	f.model.answer = map[string]any{
		"answers": []any{
			map[string]any{"key": "rating", "value": "5", "quote": "besh qo'yaman"},
			map[string]any{"key": "liked", "value": "Быстро доставили", "quote": "tez yetkazishdi"},
			map[string]any{"key": "complaint", "value": "Дорого", "quote": "juda qimmat ekan"},
		},
		"summary": "Клиент доволен, оценка пять.", "doubts": []any{"Вторая реплика клиента распознана неуверенно."},
		"callback_promised": false, "callback_time": "", "do_not_call": false,
	}
	test := f.must("POST", "/api/calls/test", map[string]any{"version_id": version})["call_id"].(string)
	f.fake.Talk("g1", "resolved", f.now, "Alo?", ">Alo", greeting, ">Ha, besh qo'yaman, tez yetkazishdi.", "Rahmat!")
	f.tick()
	call := f.must("GET", "/api/calls/"+test, nil)
	answers := call["answers"].(map[string]any)
	if call["analysis"] != "ai" || call["summary"] != "Клиент доволен, оценка пять." || answers["rating"].(map[string]any)["value"] != 5.0 ||
		answers["liked"].(map[string]any)["quote"] != "tez yetkazishdi" || answers["complaint"] != nil || len(call["doubts"].([]any)) != 2 {
		t.Fatalf("the call as the model read it: %v / %v / %v", call["analysis"], answers, call["doubts"])
	}
	asked := f.model.asked[0]
	if asked["store"] != false || asked["model"] != "gemini-flash-latest" || !strings.Contains(asked["input"].(string), "caller: Ha, besh qo'yaman") {
		t.Fatalf("what the model was asked: %v", asked)
	}

	// a person corrects an answer; a second reading keeps the correction
	f.must("PUT", "/api/calls/"+test+"/answers", map[string]any{"rating": 4})
	f.model.answer["summary"] = "Клиент доволен."
	call = f.must("POST", "/api/calls/"+test+"/analyze", nil)
	if a := call["answers"].(map[string]any)["rating"].(map[string]any); a["value"] != 4.0 || a["edited"] != true || call["summary"] != "Клиент доволен." {
		t.Fatalf("after a second reading: %v", call["answers"])
	}

	// the model does not answer: the call is closed with the plain checks
	f.model.down = true
	second := f.must("POST", "/api/calls/test", map[string]any{"version_id": version})["call_id"].(string)
	f.fake.Talk("g2", "resolved", f.now, "Alo?", ">Alo", greeting, ">Uch", "Rahmat!")
	f.tick()
	call = f.must("GET", "/api/calls/"+second, nil)
	if call["settled"] != true || call["analysis"] != "failed" || call["analysis_error"] != "rate_limit_exceeded" || call["checks"].(map[string]any)["ai_disclosed"] != true {
		t.Fatalf("a call the model did not read: %v", call)
	}
	f.refused("POST", "/api/calls/"+second+"/analyze", nil, "ai_failed")

	f.must("DELETE", "/api/ai", nil)
	f.refused("POST", "/api/calls/"+second+"/analyze", nil, "ai_not_connected")

	// a campaign counts its people's answers
	f.must("PUT", "/api/ai", map[string]any{"key": geminiKey})
	f.model.down = false
	f.must("POST", "/api/calls/"+test+"/verdict", map[string]any{"accepted": true})
	list := f.upload("l.csv", "phone\n901110001\n901110002\n")
	campaign := f.must("POST", "/api/campaigns", map[string]any{"name": "c", "version_id": version, "list_id": list["id"]})["campaign"].(map[string]any)["id"].(string)
	f.must("POST", "/api/campaigns/"+campaign+"/start", nil)
	f.tick()
	f.fake.Talk("g3", "resolved", f.now, "Alo?", ">Alo", greeting, ">Ha, besh qo'yaman, tez yetkazishdi.", "Rahmat!")
	f.fake.Talk("g4", "resolved", f.now, "Alo?", ">Alo", greeting, ">Ha, besh qo'yaman, tez yetkazishdi.", "Rahmat!")
	f.tick()
	stats := f.must("GET", "/api/campaigns/"+campaign, nil)["form"].([]any)
	rating := stats[0].(map[string]any)
	if rating["key"] != "rating" || rating["answered"] != 2.0 || rating["average"] != 5.0 || rating["values"].([]any)[4].(map[string]any)["n"] != 2.0 {
		t.Fatalf("the campaign's answers counted: %v", stats)
	}
}
