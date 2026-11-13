package crm_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/crm"
	"github.com/Islomov49/gapi-voice-studio/server/internal/crm/crmtest"
	"github.com/Islomov49/gapi-voice-studio/server/internal/model"
)

func TestBitrix24(t *testing.T) {
	portal := crmtest.NewBitrix()
	defer portal.Close()
	portal.Deals = []crmtest.Deal{{ID: "7", Title: "Стол", Stage: "NEW", Contact: "70", Assigned: "5"}, {ID: "8", Title: "Стул", Stage: "NEW", Contact: "0"}, {ID: "9", Stage: "WON", Contact: "70"}}
	portal.Contacts["70"] = [2]string{"Ali", "+998 90 111 00 01"}
	ctx := context.Background()
	b := crm.NewBitrix24(portal.URL, portal.Secret())

	portal.Busy = true // the first answer is "over the limit": the connector asks again
	if name, err := b.Account(ctx); err != nil || name == "" {
		t.Fatalf("account: %q %v", name, err)
	}
	if _, err := crm.NewBitrix24(portal.URL, "/rest/1/wrong/").Account(ctx); err == nil {
		t.Fatal("a wrong webhook was accepted")
	}
	pipelines, err := b.Pipelines(ctx)
	if err != nil || len(pipelines) != 3 || pipelines[0].Entity != "lead" || pipelines[1].ID != "0" || pipelines[1].Stages[1].ID != "WON" || pipelines[2].Stages[0].ID != "C3:NEW" {
		t.Fatalf("pipelines: %+v %v", pipelines, err)
	}
	cards, err := b.Cards(ctx, "deal", "0", "NEW", 100)
	if err != nil || len(cards) != 2 || cards[0].Name != "Ali" || cards[0].Phone != "+998 90 111 00 01" || cards[0].Responsible != "5" || cards[0].Fields["amount"] != "150000" || cards[1].Phone != "" {
		t.Fatalf("cards: %+v %v", cards, err)
	}
	if err := b.Note(ctx, "deal", "7", "итог"); err != nil {
		t.Fatal(err)
	}
	if err := b.Move(ctx, "deal", "7", "0", "WON"); err != nil {
		t.Fatal(err)
	}
	who, err := b.Responsible(ctx, "deal", "7")
	if err != nil || who != "5" {
		t.Fatalf("responsible: %q %v", who, err)
	}
	if err := b.Task(ctx, "deal", "7", who, "Перезвонить клиенту", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"comment deal 7: итог", "move deal 7 WON", "todo 2 7 for 5: Перезвонить клиенту"}; !slices.Equal(portal.Log(), want) {
		t.Fatalf("written: %v", portal.Log())
	}

	if base, secret, err := crm.SplitBitrixWebhook(" https://namuna.bitrix24.kz/rest/1/abc123def/profile "); err != nil || base != "https://namuna.bitrix24.kz" || secret != "/rest/1/abc123def/" {
		t.Fatalf("webhook: %q %q %v", base, secret, err)
	}
	if _, _, err := crm.SplitBitrixWebhook("https://namuna.bitrix24.kz/crm/deal/"); err == nil {
		t.Fatal("an address that is not a webhook was accepted")
	}
}

func TestAmoCRM(t *testing.T) {
	account := crmtest.NewAmo()
	defer account.Close()
	ctx := context.Background()
	a := crm.NewAmoCRM(account.URL, crmtest.AmoToken)

	if name, err := a.Account(ctx); err != nil || name != "Namuna" {
		t.Fatalf("account: %q %v", name, err)
	}
	if _, err := crm.NewAmoCRM(account.URL, "another token").Account(ctx); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("a wrong token: %v", err)
	}
	pipelines, err := a.Pipelines(ctx)
	if err != nil || len(pipelines) != 1 || pipelines[0].ID != "10" || pipelines[0].Stages[1].ID != "142" {
		t.Fatalf("pipelines: %+v %v", pipelines, err)
	}
	cards, err := a.Cards(ctx, "lead", "10", "100", 100)
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	if err != nil || len(cards) != 2 || cards[0].ID != "501" || cards[0].Phone != "+998902221501" || cards[0].Name != "Mijoz 1501" || cards[0].Responsible != "77" || cards[0].Fields["amount"] != "90000" {
		t.Fatalf("cards: %+v %v", cards, err)
	}
	if none, err := a.Cards(ctx, "lead", "10", "999", 100); err != nil || len(none) != 0 {
		t.Fatalf("an empty stage: %v %v", none, err)
	}
	due := time.Unix(1800000000, 0)
	for _, err := range []error{a.Note(ctx, "lead", "501", "итог"), a.Move(ctx, "lead", "501", "10", "142"), a.Task(ctx, "lead", "501", "77", "Перезвонить", due)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"note 501 common: итог", "move 501 to 10/142", "task leads 501 for 77: Перезвонить"}; !slices.Equal(account.Log(), want) {
		t.Fatalf("written: %v", account.Log())
	}
	for in, want := range map[string]string{"namuna": "https://namuna.amocrm.ru", "namuna.kommo.com": "https://namuna.kommo.com", "https://namuna.amocrm.ru/leads/": "https://namuna.amocrm.ru"} {
		if got, err := crm.AmoBase(in); err != nil || got != want {
			t.Errorf("AmoBase(%q) = %q %v", in, got, err)
		}
	}
}

func TestNotes(t *testing.T) {
	note := crm.CallNote("Фидбэк", "no_answer", 3, 0, "")
	if note != "Голосовой агент Gapi «Фидбэк»\nИтог: не ответил (попыток: 3)" {
		t.Errorf("note: %q", note)
	}
	form := crm.FormNote("Фидбэк", []model.Field{{Key: "rating", Title: "Оценка"}, {Key: "ok", Title: "Доволен"}, {Key: "empty", Title: "Пусто"}},
		map[string]model.Answer{"rating": {Value: 5.0}, "ok": {Value: true}}, "Доволен доставкой.")
	if form != "Голосовой агент Gapi «Фидбэк»: ответы\nКратко: Доволен доставкой.\nОценка: 5\nДоволен: да" {
		t.Errorf("form: %q", form)
	}
	if to, ok := crm.For(map[string]crm.StageRef{"agreed": {StageID: "WON"}, "*": {StageID: "LOSE"}}, "declined"); !ok || to.StageID != "LOSE" {
		t.Errorf("the rule for any other result: %v %v", to, ok)
	}
	if kind, entity, id, ok := crm.ParseRef(crm.Ref("bitrix24", "deal", "C3:7")); !ok || kind != "bitrix24" || entity != "deal" || id != "C3:7" {
		t.Errorf("ref: %s %s %s", kind, entity, id)
	}
}
