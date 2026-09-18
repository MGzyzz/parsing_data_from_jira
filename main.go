// Command env-release-tracker рассчитывает релизы сред и обновляет реестр.
// По умолчанию выполняется один цикл; флаг -daemon включает периодический запуск.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"

	"env-release-tracker/internal/app"
	"env-release-tracker/internal/config"
	"env-release-tracker/internal/gitlab"
	"env-release-tracker/internal/jira"
	"env-release-tracker/internal/registry"
	"env-release-tracker/internal/release"
)

// runInterval задаёт период запуска в режиме -daemon.
const runInterval = time.Hour

func main() {
	write := flag.Bool("write", false, "разрешить запись в таблицу; без флага — только считать и логировать")
	only := flag.String("env", "", "обработать только одну среду (для отладки)")
	daemon := flag.Bool("daemon", false, "не выходить, прогонять по расписанию раз в час")
	overridesPath := flag.String("config", "", "путь к overrides.yaml (по умолчанию — OVERRIDES_FILE из окружения)")
	flag.Parse()

	log := slog.Default()

	cfg, err := config.Load(config.OSLookup)
	if err != nil {
		log.Error("конфигурация", "err", err)
		os.Exit(1)
	}
	if *overridesPath != "" {
		cfg.OverridesFile = *overridesPath
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := build(ctx, cfg, *write, *only, *daemon, log)
	if err != nil {
		log.Error("сборка зависимостей", "err", err)
		os.Exit(1)
	}

	if !*daemon {
		if err := a.Run(ctx); err != nil {
			log.Error("прогон завершился с ошибкой", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := runDaemon(ctx, a, log); err != nil {
		log.Error("демон завершился с ошибкой", "err", err)
		os.Exit(1)
	}
}

// runDaemon выполняет первый цикл сразу, затем повторяет его по таймеру.
// Ошибки циклов записываются в лог. Отмена контекста завершает цикл ожидания.
func runDaemon(ctx context.Context, a *app.App, log *slog.Logger) error {
	ticker := time.NewTicker(runInterval)
	defer ticker.Stop()

	runOnce := func() {
		if err := a.Run(ctx); err != nil {
			log.Error("прогон завершился с ошибкой", "err", err)
		}
	}

	runOnce()
	for {
		select {
		case <-ctx.Done():
			// Завершение ожидания после отмены контекста, в том числе по SIGTERM.
			return nil
		case <-ticker.C:
			runOnce()
		}
	}
}

// build создаёт клиентов источников данных и экземпляр App.
func build(ctx context.Context, cfg config.Config, write bool, only string, daemon bool, log *slog.Logger) (*app.App, error) {
	overrides, err := config.LoadOverrides(cfg.OverridesFile)
	if err != nil {
		return nil, fmt.Errorf("overrides: %w", err)
	}
	// Одиночный прогон начинает с пустой памятью, и interval ни разу не сработает:
	// среды опрашиваются при каждом запуске. Молча обещать реже — хуже, чем сказать.
	if envs := overrides.IntervalEnvironments(); !daemon && len(envs) > 0 {
		log.Warn("interval из overrides действует только в режиме -daemon, среды опрашиваются при каждом запуске", "envs", envs)
	}

	googleOpt, err := googleClientOption(ctx, cfg.Sheet.Credentials)
	if err != nil {
		return nil, fmt.Errorf("доступ к Google: %w", err)
	}
	reg, err := registry.New(ctx, registry.Config{
		SpreadsheetID: cfg.Sheet.ID,
		Range:         cfg.Sheet.Range,
		Statuses:      cfg.Sheet.Statuses,
		Logger:        log,
	}, googleOpt)
	if err != nil {
		return nil, fmt.Errorf("клиент реестра: %w", err)
	}

	jiraHTTP := &http.Client{Transport: jira.Authenticated(cfg.Jira.URL, cfg.Jira.Token, jira.ReadOnly(nil)), Timeout: 30 * time.Second}
	jiraCli := jira.New(jira.Config{URL: cfg.Jira.URL, JQL: cfg.Jira.JQL}, jiraHTTP)

	var collector app.Collector = gitlab.Stub{}
	if cfg.CollectorMode == "gitlab" {
		collector, err = gitlab.New(gitlab.Config{
			URL: cfg.GitLab.URL, Token: cfg.GitLab.Token,
			ProjectID: cfg.GitLab.ProjectID, Ref: cfg.GitLab.Ref,
			PollInterval: cfg.GitLab.PollInterval, Scenario: cfg.GitLab.Scenario,
		}, nil, log)
		if err != nil {
			return nil, fmt.Errorf("клиент GitLab: %w", err)
		}
	}

	return app.New(reg, collector, jiraCli, overrides, app.Config{
		Concurrency:     cfg.Concurrency,
		PipelineTimeout: cfg.PipelineTimeout,
		Release: release.Config{
			MinorOffset:  cfg.Release.MinorOffset,
			CoreServices: cfg.Release.CoreServices,
		},
		Write: write,
		Only:  only,
	}, log), nil
}

// googleTokenPath задаёт путь к токену, сохранённому командой googleauth.
// Используется только для OAuth-входа пользователя.
const googleTokenPath = "secrets/token.json"

// googleClientOption создаёт параметры авторизации Sheets по типу credentials.
// Для service account используется ключ, для installed-app — сохранённый OAuth-токен.
func googleClientOption(ctx context.Context, credentialsPath string) (option.ClientOption, error) {
	raw, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", credentialsPath, err)
	}

	var probe struct {
		Type      string          `json:"type"`
		Installed json.RawMessage `json:"installed"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("разбор %s: %w", credentialsPath, err)
	}

	switch {
	case probe.Type == "service_account":
		return option.WithCredentialsFile(credentialsPath), nil

	case len(probe.Installed) > 0:
		oauthCfg, err := google.ConfigFromJSON(raw, sheets.SpreadsheetsScope)
		if err != nil {
			return nil, fmt.Errorf("разбор OAuth client secret: %w", err)
		}
		tokenRaw, err := os.ReadFile(googleTokenPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s не найден — сначала: go run ./cmd/googleauth", googleTokenPath)
		}
		if err != nil {
			return nil, fmt.Errorf("чтение %s: %w", googleTokenPath, err)
		}
		var tok oauth2.Token
		if err := json.Unmarshal(tokenRaw, &tok); err != nil {
			return nil, fmt.Errorf("разбор %s: %w", googleTokenPath, err)
		}
		return option.WithTokenSource(oauthCfg.TokenSource(ctx, &tok)), nil

	default:
		return nil, fmt.Errorf("%s: не похоже ни на service account, ни на OAuth client secret", credentialsPath)
	}
}
