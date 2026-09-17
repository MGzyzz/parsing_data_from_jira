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

// hfTable — HF в формате задач с сентября 2026 (по образцу DOPS-4920):
// среды заданы вики-таблицей, одна среда зачёркнута, теги оформлены ссылками.
const hfTable = `
*Release:*
 # HF | 15 Сентября - [https://jira.example/projects/PROJ/versions/1]

*Date:* 15.09.2026

*Environments:*
||№||Environment||Время||
|1|dunga-prod|20:00|
|2|kpo-prod|20:00|
|37|{-}baiterek-prod{-}(Не накатывать)|22:00|

*Actual environment list:*

*Projects:*
 # redo-front: main-1.29.22
 # redo-backend: [main-1.29.17|https://gitlab.example/redo/redo-backend/-/tags/main-1.29.17]
`

func TestParseIssueHotfixWithEnvironmentTable(t *testing.T) {
	got, ok := ParseIssue("PROJ-1", "HF Release 15 Сентября (Кроме KEGOC & KEGOC-TEST & baiterek-prod)", hfTable, time.Time{})
	if !ok {
		t.Fatal("HF с таблицей сред не распознан")
	}
	if len(got.Skipped) != 0 {
		t.Errorf("Skipped = %q, хочу пусто", got.Skipped)
	}

	for _, env := range []string{"dunga-prod", "kpo-prod"} {
		if v := got.TagsFor(env)["redo-backend"].Version; v.Minor != 29 || v.Patch != 17 {
			t.Errorf("%s redo-backend = %+v, хочу 1.29.17", env, v)
		}
	}
	// Зачёркнутая среда не катилась: HF к ней не относится.
	if tags := got.TagsFor("baiterek-prod"); len(tags) != 0 {
		t.Errorf("baiterek-prod получила состав зачёркнутой строки: %v", tags)
	}
	// Заголовок таблицы — не среда.
	if len(got.EnvTags) != 2 {
		t.Errorf("сред %d, хочу 2 (заголовок и зачёркнутая строка не в счёт): %v", len(got.EnvTags), got.EnvTags)
	}
}

func TestStruckEnvironmentInListIsExcluded(t *testing.T) {
	description := "Environments:\n1. prod-a\n8. {-}prod-b{*}({*}{-}{*}Накатили уже){*}\nProjects:\n# backend: main-1.29.0"
	got, ok := ParseIssue("PROJ-1", "Release 68", description, time.Time{})
	if !ok {
		t.Fatal("задача не распознана")
	}
	if _, found := got.EnvTags["prod-b"]; found || len(got.EnvTags) != 1 {
		t.Errorf("EnvTags = %v, хочу только prod-a", got.EnvTags)
	}
}

func TestUnknownBoldHeaderClosesProjects(t *testing.T) {
	// После Projects в задачах идут секции Reindex, Patch, Config и подписи
	// From / Release Notes. Их строки похожи на сервисы, но составом не являются.
	description := `
*Projects:*
 # redo-backend: main-1.29.17

*Reindex:*
 # nuxeo — после migrateEsedoIdToString.js

*Patch:*
 # redo-backend: setRegDataForDocumentPointPatch

*Доп. условия:*
 # Теги не новее PROJ-2: backend 1.29.17 (не 1.29.18)

*From:* main

*Release Notes:* [https://notes.example/page]
`
	got, ok := ParseIssue("PROJ-1", "Release 68", description, time.Time{})
	if !ok {
		t.Fatal("задача не распознана")
	}
	if len(got.Skipped) != 0 {
		t.Errorf("Skipped = %q, хочу пусто: строки вне Projects не разбираются", got.Skipped)
	}
	if tags := got.TagsFor("prod-a"); len(tags) != 1 || tags["redo-backend"].Version.Patch != 17 {
		t.Errorf("состав = %v, хочу только redo-backend 1.29.17", tags)
	}
}

func TestBoldProjectLineIsNotHeader(t *testing.T) {
	// Жирная строка сервиса похожа на заголовок, но двоеточие не перед звёздочкой.
	got, ok := ParseIssue("PROJ-1", "Release 68", "*Projects:*\n*backend: main-1.29.0*\n*front: main-1.29.0*", time.Time{})
	if !ok || len(got.TagsFor("prod-a")) != 2 {
		t.Fatalf("жирные строки сервисов потеряны: ok=%v task=%+v", ok, got)
	}
}
