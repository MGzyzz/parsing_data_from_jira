// Package app выполняет чтение реестра, сбор образов, расчёт релизов
// и обновление таблицы. Доступ к источникам данных задаётся интерфейсами.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"env-release-tracker/internal/config"
	"env-release-tracker/internal/jira"
	"env-release-tracker/internal/registry"
	"env-release-tracker/internal/release"
)

// Registry — часть registry.Client, нужная оркестрации.
type Registry interface {
	List(ctx context.Context) ([]registry.Row, error)
	Update(ctx context.Context, updates []registry.Update) (int, error)
}

// Collector получает снимок версий сервисов указанной среды.
type Collector interface {
	Collect(ctx context.Context, environment string) (release.EnvState, error)
}

// JiraFetcher — часть jira.Client, нужная оркестрации.
type JiraFetcher interface {
	Fetch(ctx context.Context) ([]jira.ReleaseTask, error)
}

// Store хранит время успешного сбора для проверки overrides.interval.
type Store interface {
	Get(environment string) (time.Time, bool)
	Set(environment string, at time.Time)
}

// MemoryStore — Store по умолчанию: в памяти процесса, без персистентности.
type MemoryStore struct {
	mu   sync.Mutex
	data map[string]time.Time
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{data: map[string]time.Time{}} }

func (s *MemoryStore) Get(environment string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data[environment]
	return t, ok
}

func (s *MemoryStore) Set(environment string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[environment] = at
}

// Config — параметры одного прогона.
type Config struct {
	Concurrency     int
	PipelineTimeout time.Duration
	Release         release.Config
	// Write разрешает запись в таблицу. Без него (-write не передан)
	// прогон только считает и логирует — dry-run по умолчанию.
	Write bool
	// Only — одна среда для отладки (-env). Пусто — все строки реестра.
	Only string
}

type App struct {
	registry  Registry
	collector Collector
	jiraCli   JiraFetcher
	overrides config.Overrides
	cfg       Config
	log       *slog.Logger
	store     Store
	now       func() time.Time
}

// New собирает оркестрацию. log == nil — берётся slog.Default().
func New(reg Registry, collector Collector, jiraCli JiraFetcher, overrides config.Overrides, cfg Config, log *slog.Logger) *App {
	if log == nil {
		log = slog.Default()
	}
	return &App{
		registry:  reg,
		collector: collector,
		jiraCli:   jiraCli,
		overrides: overrides,
		cfg:       cfg,
		log:       log,
		store:     NewMemoryStore(),
		now:       time.Now,
	}
}

// outcome — что получилось по одной строке реестра.
type outcome struct {
	row    registry.Row
	result release.Result
	err    error  // сбой сбора или расчёта — предупреждение в лог
	skip   string // плановый пропуск (интервал) — не предупреждение
}

// Run выполняет один цикл обработки сред. Расписание задаёт вызывающий код.
func (a *App) Run(ctx context.Context) error {
	rows, err := a.registry.List(ctx)
	if err != nil {
		return fmt.Errorf("чтение реестра: %w", err)
	}

	// Задачи Jira загружаются один раз и используются для всех сред.
	tasks, err := a.jiraCli.Fetch(ctx)
	if err != nil {
		return fmt.Errorf("чтение задач Jira: %w", err)
	}

	rows = a.filterRows(rows)
	outcomes := a.collectAll(ctx, rows, tasks)

	updates := make([]registry.Update, 0, len(outcomes))
	for _, o := range outcomes {
		switch {
		case o.skip != "":
			a.log.Info("среда пропущена", "env", o.row.Name, "reason", o.skip)
		case o.err != nil:
			a.log.Warn("сбор не удался", "env", o.row.Name, "err", o.err)
		case !o.result.Defined():
			// Если релиз не определён, значение в реестре сохраняется.
			a.log.Info("релиз не определён", "env", o.row.Name, "details", o.result.Details)
		default:
			a.log.Info("релиз определён", "env", o.row.Name, "release", o.result.Cell(),
				"minor_mismatch", o.result.MinorMismatch, "jira_mismatch", o.result.JiraMismatch, "details", o.result.Details)
			if o.result.MinorMismatch || o.result.JiraMismatch {
				a.log.Warn("расхождение версий", "env", o.row.Name, "details", o.result.Details)
			}
			updates = append(updates, registry.Update{Row: o.row, Value: o.result.Cell()})
		}
	}

	if !a.cfg.Write {
		a.log.Info("dry-run: запись пропущена", "would_write", len(updates))
		return nil
	}

	n, err := a.registry.Update(ctx, updates)
	if err != nil {
		return fmt.Errorf("запись реестра: %w", err)
	}
	a.log.Info("реестр обновлён", "cells", n)
	return nil
}

// filterRows применяет -env и overrides.skip до запуска пула воркеров:
// пропущенные среды не должны занимать место в CONCURRENCY.
func (a *App) filterRows(rows []registry.Row) []registry.Row {
	out := make([]registry.Row, 0, len(rows))
	for _, r := range rows {
		if a.cfg.Only != "" && r.Name != a.cfg.Only {
			continue
		}
		if ov := a.overrides.For(r.Name); ov.Skipped() {
			a.log.Info("среда пропущена по overrides", "env", r.Name, "reason", ov.Skip)
			continue
		}
		out = append(out, r)
	}
	return out
}

// collectAll опрашивает среды пулом воркеров ограниченного размера.
func (a *App) collectAll(ctx context.Context, rows []registry.Row, tasks []jira.ReleaseTask) []outcome {
	outcomes := make([]outcome, len(rows))
	sem := make(chan struct{}, max(a.cfg.Concurrency, 1))
	var wg sync.WaitGroup

	for i, row := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, row registry.Row) {
			defer wg.Done()
			defer func() { <-sem }()
			outcomes[i] = a.collectOne(ctx, row, tasks)
		}(i, row)
	}
	wg.Wait()
	return outcomes
}

// collectOne проверяет интервал, собирает образы и рассчитывает релиз.
// Первый вызов Compute определяет номер для выбора базы Jira,
// второй уточняет HF и признаки расхождения с выбранной базой.
func (a *App) collectOne(ctx context.Context, row registry.Row, tasks []jira.ReleaseTask) outcome {
	ov := a.overrides.For(row.Name)

	if ov.Interval > 0 {
		if last, ok := a.store.Get(row.Name); ok && a.now().Sub(last) < ov.Interval {
			return outcome{row: row, skip: fmt.Sprintf("интервал %s ещё не прошёл", ov.Interval)}
		}
	}

	environment := row.Name
	if ov.Environment != "" {
		environment = ov.Environment
	}

	cctx, cancel := context.WithTimeout(ctx, a.cfg.PipelineTimeout)
	defer cancel()

	var state release.EnvState
	var err error
	if ov.TagsName != "" {
		collector, ok := a.collector.(interface {
			CollectWithRunner(context.Context, string, string) (release.EnvState, error)
		})
		if !ok {
			return outcome{row: row, err: fmt.Errorf("сборщик не поддерживает tags_name")}
		}
		state, err = collector.CollectWithRunner(cctx, environment, ov.TagsName)
	} else {
		state, err = a.collector.Collect(cctx, environment)
	}
	if err != nil {
		return outcome{row: row, err: fmt.Errorf("сбор образов: %w", err)}
	}
	a.store.Set(row.Name, a.now())

	draft := release.Compute(state, nil, a.cfg.Release)
	base := baselineFor(tasks, draft.Release, row.Name)
	return outcome{row: row, result: release.Compute(state, base, a.cfg.Release)}
}

// baselineFor выбирает последнюю по дате базовую задачу релиза и последующие HF.
// Для среды используется её блок тегов или общий состав, если отдельного блока нет.
func baselineFor(tasks []jira.ReleaseTask, number int, environment string) *release.Baseline {
	if number == 0 {
		return nil
	}
	var selected *jira.ReleaseTask
	for i := range tasks {
		t := &tasks[i]
		if t.IsHF || t.Number != number || len(t.TagsFor(environment)) == 0 {
			continue
		}
		// Стабильный выбор при нескольких задачах одного релиза.
		if selected == nil || t.Date.After(selected.Date) || (t.Date.Equal(selected.Date) && t.Key < selected.Key) {
			selected = t
		}
	}
	if selected == nil {
		return nil
	}
	base := &release.Baseline{Number: selected.Number, Tags: selected.TagsFor(environment)}
	for _, t := range tasks {
		if t.IsHF && t.Date.After(selected.Date) {
			base.Hotfixes = append(base.Hotfixes, t.TagsFor(environment))
		}
	}
	return base
}
