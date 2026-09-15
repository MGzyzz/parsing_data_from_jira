// Package jira читает задачи релизов. Только чтение: сервис не меняет в Jira
// ничего, и это обеспечивается guard'ом в транспорте, а не дисциплиной.
package jira

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"env-release-tracker/internal/tag"
)

// ReleaseTask — разобранная задача релиза или хотфикса.
type ReleaseTask struct {
	Key      string
	Summary  string
	Number   int // номер релиза; 0 у хотфиксов, см. releaseNumber
	Fraction int // дробная часть номера: Release 57/3 -> 3
	IsHF     bool
	Date     time.Time
	// EnvTags хранит состав по средам. Ключ "" — основной поток: теги,
	// заданные до любого блока Environments, действуют на все среды,
	// у которых нет своего блока.
	EnvTags map[string]map[string]tag.Tag
	// Skipped — строки, похожие на сервис, но с нечитаемым тегом.
	// Разбор они не роняют, но должны попасть в лог.
	Skipped []string
}

const mainFlow = ""

// TagsFor возвращает состав для среды: её собственный блок, иначе основной поток.
func (t ReleaseTask) TagsFor(environment string) map[string]tag.Tag {
	if tags, ok := t.EnvTags[environment]; ok {
		return tags
	}
	return t.EnvTags[mainFlow]
}

var (
	// hotfix ищет пометку HF отдельным словом.
	hotfix = regexp.MustCompile(`(?i)\bhf\b`)

	// releaseNumber требует цифры сразу после слова Release. Этим отсеивается
	// шум под JQL: "Release PLAT-7484", "Main / Release: проставить пароль",
	// "Включить конфиг на release" — номера там нет.
	releaseNumber = regexp.MustCompile(`(?i)\brelease\s+(\d+)(?:/(\d+))?`)

	// sectionHeader распознаёт заголовки секций описания в обоих языках.
	//
	// Без \b: в Go это граница ASCII-слова, и после кириллического "Среды"
	// она не срабатывает — русские заголовки переставали распознаваться.
	sectionHeader = regexp.MustCompile(`(?i)^(environments|среды|projects|проекты|date|дата|releases|релизы|hf)[^:]*:\s*(.*)$`)

	dateFormats = []string{"2006-01-02", "02.01.2006", "02/01/2006"}
)

// ParseIssue разбирает задачу. Второе значение — релизная ли она: под JQL
// summary ~ "Release" попадает много постороннего, и отличать одно от другого
// приходится сервису, потому что конвенцию для задач накатки не вводят.
func ParseIssue(key, summary, description string, created time.Time) (ReleaseTask, bool) {
	task := ReleaseTask{
		Key:     key,
		Summary: summary,
		IsHF:    hotfix.MatchString(summary),
		Date:    created,
		EnvTags: map[string]map[string]tag.Tag{},
	}

	// У хотфиксов в заголовке стоит дата: "HF Release 11 Сентября". Принять
	// 11 за номер релиза значило бы сверяться с несуществующим релизом.
	if !task.IsHF {
		if m := releaseNumber.FindStringSubmatch(summary); m != nil {
			task.Number, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				task.Fraction, _ = strconv.Atoi(m[2])
			}
		}
	}

	parseDescription(&task, description)

	// Релизная задача обязана иметь номер или пометку HF и хотя бы один
	// разбираемый тег: иначе сверять нечего.
	if task.Number == 0 && !task.IsHF {
		return ReleaseTask{}, false
	}
	if len(task.EnvTags) == 0 {
		return ReleaseTask{}, false
	}
	return task, true
}

// parseDescription проходит описание построчно, разбирая секции.
//
// Блоков Environments + Projects в одной задаче бывает несколько: в DOPS-4820
// основной поток идёт на main-1.29.0, а prod-holding и prod-idfrk на
// release-1.29.1. Поэтому Projects относится к последнему встреченному
// Environments, а до первого такого блока — к основному потоку.
func parseDescription(task *ReleaseTask, description string) {
	var (
		inProjects bool
		targets    = []string{mainFlow}
	)

	for _, line := range strings.Split(description, "\n") {
		clean := strings.TrimSpace(strings.ReplaceAll(line, "*", ""))

		if m := sectionHeader.FindStringSubmatch(clean); m != nil {
			section, rest := strings.ToLower(m[1]), strings.TrimSpace(m[2])

			switch section {
			case "environments", "среды":
				inProjects = false
				if envs := splitEnvironments(rest); len(envs) > 0 {
					targets = envs
				}
			case "projects", "проекты":
				inProjects = true
			case "date", "дата":
				inProjects = false
				if d, ok := parseDate(rest); ok {
					task.Date = d
				}
			default:
				inProjects = false
			}
			continue
		}

		if !inProjects {
			continue
		}

		parsed, ok, err := tag.ParseProjectLine(line)
		if err != nil {
			task.Skipped = append(task.Skipped, strings.TrimSpace(line))
			continue
		}
		if !ok {
			continue
		}
		for _, env := range targets {
			if task.EnvTags[env] == nil {
				task.EnvTags[env] = map[string]tag.Tag{}
			}
			task.EnvTags[env][parsed.Service] = parsed
		}
	}
}

// splitEnvironments разбирает список сред: "prod-holding, prod-idfrk"
// или нумерованный "1. mod-prod".
func splitEnvironments(s string) []string {
	var envs []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(listPrefix.ReplaceAllString(strings.TrimSpace(part), ""))
		if part != "" {
			envs = append(envs, part)
		}
	}
	return envs
}

var listPrefix = regexp.MustCompile(`^(?:#+|\d+\.)\s*`)

func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range dateFormats {
		if d, err := time.Parse(layout, s); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}
