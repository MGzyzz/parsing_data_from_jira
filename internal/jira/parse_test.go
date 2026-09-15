package jira

import (
	"testing"
	"time"
)

// dops4820 — описание задачи Release 68 из ТЗ.
// Основной поток идёт на main-1.29.0, а prod-holding и prod-idfrk на release-1.29.1.
const dops4820 = `
*Date:* 2026-07-20

*Projects:*
 # nuxeo: main-1.29.0
 # backend: main-1.29.0
 # camunda: main-1.29.0
 # front: main-1.29.0
 # integration: main-1.29.0
 # esedo-gateway:
 # notification: main-1.29.0
 # -enbek-integration: main-1.29.0-
 # redo-email: main-1.29.0

*Environments:* prod-holding, prod-idfrk

*Projects:*
 # nuxeo: release-1.29.1
 # backend: release-1.29.1
`

func TestParseIssueRelease(t *testing.T) {
	got, ok := ParseIssue("DOPS-4820", "Release 68 | 02 Июля (1 очередь)", dops4820, time.Time{})
	if !ok {
		t.Fatal("задача релиза не распознана")
	}

	if got.Number != 68 {
		t.Errorf("Number = %d, хочу 68", got.Number)
	}
	if got.IsHF {
		t.Error("IsHF = true, хочу false")
	}
	if want := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC); !got.Date.Equal(want) {
		t.Errorf("Date = %v, хочу %v (поле Date из описания)", got.Date, want)
	}

	main := got.TagsFor("prod-qazsu")
	if len(main) != 7 {
		t.Fatalf("основной поток: тегов %d, хочу 7 (пустой и зачёркнутый пропущены)", len(main))
	}
	if v := main["redo-backend"].Version; v.Minor != 29 || v.Patch != 0 {
		t.Errorf("redo-backend = %+v, хочу 1.29.0", v)
	}
	if _, found := main["redo-esedo-gateway"]; found {
		t.Error("сервис с пустым тегом попал в состав")
	}
	if _, found := main["redo-enbek-integration"]; found {
		t.Error("зачёркнутый сервис попал в состав")
	}

	// У сред со своим блоком Environments теги свои.
	holding := got.TagsFor("prod-holding")
	if v := holding["redo-backend"].Version; v.Minor != 29 || v.Patch != 1 {
		t.Errorf("prod-holding redo-backend = %+v, хочу 1.29.1", v)
	}
	if b := holding["redo-backend"].Branch; b != "release" {
		t.Errorf("prod-holding redo-backend ветка = %q, хочу release", b)
	}
}

func TestParseIssueFallsBackToCreated(t *testing.T) {
	created := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	got, ok := ParseIssue("DOPS-1", "Release 60", "*Projects:*\n # backend: main-1.21.0\n", created)
	if !ok {
		t.Fatal("задача не распознана")
	}
	if !got.Date.Equal(created) {
		t.Errorf("Date = %v, хочу %v: поля Date в описании нет", got.Date, created)
	}
}

func TestParseIssueHotfix(t *testing.T) {
	// Ловушка формата: в заголовке HF "Release 11 Сентября" — это дата,
	// а не номер релиза. Принять 11 за номер значило бы сверять теги
	// с несуществующим релизом.
	got, ok := ParseIssue("DOPS-4892", "HF Release 11 Сентября для всех сред",
		"*Projects:*\n # redo-backend: main-1.29.13\n # redo-camunda: main-1.29.12\n", time.Time{})
	if !ok {
		t.Fatal("задача HF не распознана")
	}
	if !got.IsHF {
		t.Error("IsHF = false, хочу true")
	}
	if got.Number != 0 {
		t.Errorf("Number = %d, хочу 0: в заголовке HF стоит дата, а не номер релиза", got.Number)
	}
	if v := got.TagsFor("любая")["redo-backend"].Version; v.Patch != 13 {
		t.Errorf("redo-backend патч = %d, хочу 13", v.Patch)
	}
}

func TestParseIssueHotfixWithMonthInGenitive(t *testing.T) {
	got, ok := ParseIssue("DOPS-2", "HF для среды Release 12 Июня BAITEREK",
		"*Projects:*\n # backend: main-1.29.5\n", time.Time{})
	if !ok {
		t.Fatal("задача HF не распознана")
	}
	if got.Number != 0 {
		t.Errorf("Number = %d, хочу 0: 12 Июня это дата", got.Number)
	}
}

func TestParseIssueFractionalRelease(t *testing.T) {
	// Дробные релизы 57/3: номер берём целой частью, дробь сохраняем -
	// как их показывать, заказчик ещё не решил.
	got, ok := ParseIssue("DOPS-3", "Release 57/3 | 10 Мая", "*Projects:*\n # backend: main-1.18.0\n", time.Time{})
	if !ok {
		t.Fatal("задача не распознана")
	}
	if got.Number != 57 {
		t.Errorf("Number = %d, хочу 57", got.Number)
	}
	if got.Fraction != 3 {
		t.Errorf("Fraction = %d, хочу 3", got.Fraction)
	}
}

func TestParseIssueRussianSections(t *testing.T) {
	// Заказчик присылал состав среды с русскими заголовками и нумерованным списком.
	desc := `
Среды: 1. mod-prod
Проекты:
1. redo-nuxeo: main-1.26.1
2. redo-backend: main-1.26.4
`
	got, ok := ParseIssue("DOPS-5", "Release 65 | 02 Июля (1 очередь)", desc, time.Time{})
	if !ok {
		t.Fatal("задача не распознана")
	}
	tags := got.TagsFor("mod-prod")
	if v := tags["redo-backend"].Version; v.Minor != 26 || v.Patch != 4 {
		t.Errorf("mod-prod redo-backend = %+v, хочу 1.26.4", v)
	}
	if len(got.TagsFor("другая-среда")) != 0 {
		t.Error("теги блока со своим Environments не должны попадать в основной поток")
	}
}

func TestParseIssueRejectsNoise(t *testing.T) {
	// Под JQL summary ~ "Release" попадает много постороннего.
	noise := []struct{ key, summary, desc string }{
		{"DOPS-10", "Main / Release: проставить всем пользователям единый пароль", "текст без секций"},
		{"DOPS-11", "Включить конфиг hazelcast на release", "*Projects:*\n # backend: main-1.29.0\n"},
		{"DOPS-12", "Release PLAT-7484 для сред prod-qazsu", "*Projects:*\n # backend: main-1.29.0\n"},
	}

	for _, n := range noise {
		t.Run(n.key, func(t *testing.T) {
			if _, ok := ParseIssue(n.key, n.summary, n.desc, time.Time{}); ok {
				t.Errorf("шум %q принят за релизную задачу", n.summary)
			}
		})
	}
}

func TestParseIssueRejectsReleaseWithoutTags(t *testing.T) {
	// Номер в заголовке есть, но разбираемых тегов нет: судить не по чему.
	if _, ok := ParseIssue("DOPS-13", "Release 68 | перенос сроков", "Обсуждение без секции Projects", time.Time{}); ok {
		t.Error("задача без тегов принята за релизную")
	}
}

func TestParseIssueCollectsUnparsedLines(t *testing.T) {
	// Нечитаемый тег не роняет разбор, но должен быть виден.
	desc := "*Projects:*\n # backend: main-1.29.0\n # front: feature-DOPS-1234\n"
	got, ok := ParseIssue("DOPS-14", "Release 68", desc, time.Time{})
	if !ok {
		t.Fatal("задача не распознана")
	}
	if len(got.Skipped) != 1 {
		t.Errorf("Skipped = %v, хочу одну строку с нечитаемым тегом", got.Skipped)
	}
}
