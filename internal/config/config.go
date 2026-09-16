// Package config собирает настройки сервиса из переменных окружения
// и файла overrides.yaml.
//
// Load принимает функцию поиска переменной, а не читает окружение сам:
// так конфиг проверяется тестами без правки окружения процесса.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — все настройки одного прогона.
type Config struct {
	GitLab          GitLab
	Jira            Jira
	Sheet           Sheet
	Release         Release
	Concurrency     int
	PipelineTimeout time.Duration
	OverridesFile   string
}

// GitLab — доступ к пайплайну collect-images.
type GitLab struct {
	URL       string
	Token     string
	ProjectID int
	Ref       string
}

// Jira — доступ к задачам релизов. Только чтение.
type Jira struct {
	URL   string
	Token string
	JQL   string
}

// Sheet — реестр сред.
//
// ID в разработке указывает на копию: запись идёт в рабочую таблицу команды,
// и отладочный прогон не должен её задеть.
type Sheet struct {
	ID          string
	Range       string
	Credentials string
	Statuses    []string // какие строки реестра обрабатываем
}

// Release — правила расчёта номера релиза.
type Release struct {
	MinorOffset  int
	CoreServices []string
}

// Lookup ищет переменную окружения. Совместима с os.LookupEnv.
type Lookup func(string) (string, bool)

// OSLookup читает настоящее окружение процесса.
func OSLookup(key string) (string, bool) { return os.LookupEnv(key) }

// Load загружает настройки и возвращает все ошибки валидации одним результатом.
func Load(env Lookup) (Config, error) {
	l := loader{env: env}

	cfg := Config{
		GitLab: GitLab{
			URL:       l.required("GITLAB_URL"),
			Token:     l.required("GITLAB_TOKEN"),
			ProjectID: l.intVal("COLLECT_IMAGES_PROJECT_ID", 0),
			Ref:       l.str("COLLECT_IMAGES_REF", "main"),
		},
		Jira: Jira{
			URL:   l.required("JIRA_URL"),
			Token: l.required("JIRA_TOKEN"),
			JQL:   l.str("JIRA_JQL", `summary ~ "Release" AND project = DevOps`),
		},
		Sheet: Sheet{
			ID:          l.required("SHEET_ID"),
			Range:       l.str("SHEET_RANGE", "A:I"),
			Credentials: l.required("GOOGLE_CREDENTIALS_JSON"),
			Statuses:    l.list("ENV_STATUSES", "active,configuring"),
		},
		Release: Release{
			MinorOffset:  l.intVal("RELEASE_MINOR_OFFSET", 39),
			CoreServices: l.list("CORE_SERVICES", "redo-nuxeo,redo-backend,redo-camunda,redo-front,redo-integration"),
		},
		Concurrency:     l.intVal("CONCURRENCY", 5),
		PipelineTimeout: l.duration("PIPELINE_TIMEOUT", 10*time.Minute),
		OverridesFile:   l.str("OVERRIDES_FILE", "overrides.yaml"),
	}

	// Для обработки сред требуется хотя бы один рабочий слот.
	if cfg.Concurrency < 1 {
		l.errs = append(l.errs, errors.New("CONCURRENCY должен быть больше нуля"))
	}
	if cfg.GitLab.ProjectID < 1 {
		l.errs = append(l.errs, errors.New("COLLECT_IMAGES_PROJECT_ID должен быть больше нуля"))
	}

	if len(l.errs) > 0 {
		return Config{}, errors.Join(l.errs...)
	}
	return cfg, nil
}

// loader копит ошибки разбора, чтобы сообщить обо всех сразу.
type loader struct {
	env  Lookup
	errs []error
}

func (l *loader) str(key, def string) string {
	if v, ok := l.env(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (l *loader) required(key string) string {
	v, ok := l.env(key)
	if !ok || strings.TrimSpace(v) == "" {
		l.errs = append(l.errs, fmt.Errorf("не задана переменная %s", key))
		return ""
	}
	return strings.TrimSpace(v)
}

func (l *loader) intVal(key string, def int) int {
	raw, ok := l.env(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q не число", key, raw))
		return def
	}
	return n
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	raw, ok := l.env(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q не длительность", key, raw))
		return def
	}
	return d
}

func (l *loader) list(key, def string) []string {
	return splitList(l.str(key, def))
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
