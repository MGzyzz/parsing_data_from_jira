// Package jira загружает и разбирает задачи релизов.
// Транспорт ReadOnly ограничивает доступ разрешёнными операциями чтения.
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
	// Skipped содержит строки с нераспознаваемыми тегами для диагностики.
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

// ParseIssue извлекает номер релиза, дату и теги из задачи.
// Второй результат указывает, распознана ли задача как релиз или HF.
func ParseIssue(key, summary, description string, created time.Time) (ReleaseTask, bool) {
	task := ReleaseTask{
		Key:     key,
		Summary: summary,
		IsHF:    hotfix.MatchString(summary),
		Date:    created,
		EnvTags: map[string]map[string]tag.Tag{},
	}

	// В заголовке HF после Release может стоять дата, например 11 Сентября.
	// Номер релиза из такого заголовка не извлекается.
	if !task.IsHF {
		if m := releaseNumber.FindStringSubmatch(summary); m != nil {
			task.Number, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				task.Fraction, _ = strconv.Atoi(m[2])
			}
		}
	}

	parseDescription(&task, description)

	// Для распознавания нужны номер релиза или HF и хотя бы один разобранный тег.
	if task.Number == 0 && !task.IsHF {
		return ReleaseTask{}, false
	}
	if len(task.EnvTags) == 0 {
		return ReleaseTask{}, false
	}
	return task, true
}

// parseDescription разбирает секции описания построчно.
// Projects относится к последнему блоку Environments;
// до первого такого блока теги сохраняются в общий состав.
func parseDescription(task *ReleaseTask, description string) {
	var (
		inProjects     bool
		inEnvironments bool
		targets        = []string{mainFlow}
	)

	for _, line := range strings.Split(description, "\n") {
		clean := strings.TrimSpace(strings.ReplaceAll(line, "*", ""))

		if m := sectionHeader.FindStringSubmatch(clean); m != nil {
			section, rest := strings.ToLower(m[1]), strings.TrimSpace(m[2])
			inEnvironments = false

			switch section {
			case "environments", "среды":
				inProjects = false
				inEnvironments = true
				targets = splitEnvironments(rest)
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

		if inEnvironments {
			targets = append(targets, splitEnvironments(clean)...)
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
		if len(targets) == 0 {
			task.Skipped = append(task.Skipped, "Projects без распознанных сред")
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
		if environmentName.MatchString(part) {
			envs = append(envs, part)
		}
	}
	return envs
}

var listPrefix = regexp.MustCompile(`^(?:#+|[-•]|\d+\.)\s*`)
var environmentName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range dateFormats {
		if d, err := time.Parse(layout, s); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}
