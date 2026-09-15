package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// full — полный набор переменных из ТЗ.
func full() map[string]string {
	return map[string]string{
		"GITLAB_URL":                "https://gitlab.example.kz",
		"GITLAB_TOKEN":              "glpat-xxx",
		"COLLECT_IMAGES_PROJECT_ID": "234",
		"COLLECT_IMAGES_REF":        "main",
		"JIRA_URL":                  "https://jira.example.kz",
		"JIRA_TOKEN":                "jira-xxx",
		"JIRA_JQL":                  `summary ~ "Release" AND project = DevOps`,
		"RELEASE_MINOR_OFFSET":      "39",
		"SHEET_ID":                  "copy-sheet-id",
		"SHEET_RANGE":               "A:I",
		"GOOGLE_CREDENTIALS_JSON":   "/run/secrets/sa.json",
		"ENV_STATUSES":              "active,configuring",
		"CORE_SERVICES":             "redo-nuxeo,redo-backend,redo-camunda,redo-front,redo-integration",
		"CONCURRENCY":               "5",
		"PIPELINE_TIMEOUT":          "10m",
	}
}

func lookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadFull(t *testing.T) {
	cfg, err := Load(lookup(full()))
	if err != nil {
		t.Fatalf("Load вернул ошибку: %v", err)
	}

	if cfg.GitLab.ProjectID != 234 {
		t.Errorf("ProjectID = %d, хочу 234", cfg.GitLab.ProjectID)
	}
	if cfg.Release.MinorOffset != 39 {
		t.Errorf("MinorOffset = %d, хочу 39", cfg.Release.MinorOffset)
	}
	if got := len(cfg.Release.CoreServices); got != 5 {
		t.Errorf("кор-сервисов %d, хочу 5", got)
	}
	if cfg.Concurrency != 5 {
		t.Errorf("Concurrency = %d, хочу 5", cfg.Concurrency)
	}
	if cfg.PipelineTimeout != 10*time.Minute {
		t.Errorf("PipelineTimeout = %v, хочу 10m", cfg.PipelineTimeout)
	}
	if got := strings.Join(cfg.Sheet.Statuses, ","); got != "active,configuring" {
		t.Errorf("Statuses = %q, хочу active,configuring", got)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	// Необязательные переменные имеют значения из ТЗ, чтобы конфиг
	// в типовой установке сводился к одним секретам.
	env := full()
	delete(env, "CONCURRENCY")
	delete(env, "PIPELINE_TIMEOUT")
	delete(env, "RELEASE_MINOR_OFFSET")
	delete(env, "COLLECT_IMAGES_REF")
	delete(env, "SHEET_RANGE")
	delete(env, "ENV_STATUSES")
	delete(env, "CORE_SERVICES")

	cfg, err := Load(lookup(env))
	if err != nil {
		t.Fatalf("Load вернул ошибку: %v", err)
	}

	if cfg.Concurrency != 5 {
		t.Errorf("Concurrency = %d, хочу 5 по умолчанию", cfg.Concurrency)
	}
	if cfg.PipelineTimeout != 10*time.Minute {
		t.Errorf("PipelineTimeout = %v, хочу 10m по умолчанию", cfg.PipelineTimeout)
	}
	if cfg.Release.MinorOffset != 39 {
		t.Errorf("MinorOffset = %d, хочу 39 по умолчанию", cfg.Release.MinorOffset)
	}
	if cfg.GitLab.Ref != "main" {
		t.Errorf("Ref = %q, хочу main по умолчанию", cfg.GitLab.Ref)
	}
	if len(cfg.Release.CoreServices) != 5 {
		t.Errorf("кор-сервисов %d, хочу 5 по умолчанию", len(cfg.Release.CoreServices))
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	// Пропущенный секрет должен называться в ошибке: иначе развёртывание
	// падает с невнятным сообщением, и причину ищут в коде.
	for _, key := range []string{"GITLAB_TOKEN", "JIRA_TOKEN", "SHEET_ID", "GOOGLE_CREDENTIALS_JSON", "GITLAB_URL", "JIRA_URL"} {
		t.Run(key, func(t *testing.T) {
			env := full()
			delete(env, key)

			_, err := Load(lookup(env))
			if err == nil {
				t.Fatalf("Load без %s прошёл без ошибки", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("ошибка %q не называет переменную %s", err, key)
			}
		})
	}
}

func TestLoadRejectsBadNumbers(t *testing.T) {
	env := full()
	env["CONCURRENCY"] = "ноль"

	if _, err := Load(lookup(env)); err == nil {
		t.Fatal("Load принял нечисловой CONCURRENCY")
	}
}

func TestLoadRejectsZeroConcurrency(t *testing.T) {
	// Нулевая параллельность остановила бы прогон молча.
	env := full()
	env["CONCURRENCY"] = "0"

	if _, err := Load(lookup(env)); err == nil {
		t.Fatal("Load принял CONCURRENCY=0")
	}
}

func TestLoadTrimsListValues(t *testing.T) {
	env := full()
	env["CORE_SERVICES"] = " redo-backend , redo-front "

	cfg, err := Load(lookup(env))
	if err != nil {
		t.Fatalf("Load вернул ошибку: %v", err)
	}
	want := []string{"redo-backend", "redo-front"}
	for i, s := range want {
		if cfg.Release.CoreServices[i] != s {
			t.Errorf("CoreServices[%d] = %q, хочу %q", i, cfg.Release.CoreServices[i], s)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overrides.yaml")
	content := `
envs:
  sk-prod:
    tags_name: "runner-sk"
  prod-devops:
    skip: "среда выключена"
  nit-adilet:
    interval: "24h"
  kegoc-test:
    environment: "kegoc"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	ov, err := LoadOverrides(path)
	if err != nil {
		t.Fatalf("LoadOverrides вернул ошибку: %v", err)
	}

	if got := ov.For("sk-prod").TagsName; got != "runner-sk" {
		t.Errorf("tags_name = %q, хочу runner-sk", got)
	}
	if !ov.For("prod-devops").Skipped() {
		t.Error("prod-devops должна пропускаться")
	}
	if got := ov.For("nit-adilet").Interval; got != 24*time.Hour {
		t.Errorf("interval = %v, хочу 24h", got)
	}
	if got := ov.For("kegoc-test").Environment; got != "kegoc" {
		t.Errorf("environment = %q, хочу kegoc", got)
	}
	if ov.For("prod-qazsu").Skipped() {
		t.Error("среда без правила не должна пропускаться")
	}
}

func TestLoadOverridesMissingFileIsNotAnError(t *testing.T) {
	// Файла может не быть: это штатная установка без особых сред.
	ov, err := LoadOverrides(filepath.Join(t.TempDir(), "нет.yaml"))
	if err != nil {
		t.Fatalf("отсутствие файла дало ошибку: %v", err)
	}
	if ov.For("что-угодно").Skipped() {
		t.Error("пустые overrides не должны никого пропускать")
	}
}

func TestLoadOverridesRejectsBadInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overrides.yaml")
	if err := os.WriteFile(path, []byte("envs:\n  a:\n    interval: \"иногда\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOverrides(path); err == nil {
		t.Fatal("LoadOverrides принял нечитаемый interval")
	}
}
