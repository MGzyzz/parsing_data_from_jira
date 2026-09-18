package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"env-release-tracker/internal/config"
	"env-release-tracker/internal/jira"
	"env-release-tracker/internal/logtest"
	"env-release-tracker/internal/registry"
	"env-release-tracker/internal/release"
	"os"

	"env-release-tracker/internal/tag"
)

// --- подставные реализации интерфейсов (§10 спеки) ---

type fakeRegistry struct {
	rows      []registry.Row
	listErr   error
	updateErr error

	mu      sync.Mutex
	updates []registry.Update
	calls   int
}

func (f *fakeRegistry) List(context.Context) ([]registry.Row, error) { return f.rows, f.listErr }

func (f *fakeRegistry) Update(_ context.Context, updates []registry.Update) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.updates = updates
	if f.updateErr != nil {
		return 0, f.updateErr
	}
	return len(updates), nil
}

type fakeCollector struct {
	byEnv map[string]release.EnvState
	errs  map[string]error
	block <-chan struct{} // если задан, Collect ждёт его перед возвратом

	mu          sync.Mutex
	calls       []string
	inFlight    int32
	maxInFlight int32
}

func (f *fakeCollector) Collect(_ context.Context, environment string) (release.EnvState, error) {
	f.mu.Lock()
	f.calls = append(f.calls, environment)
	f.mu.Unlock()

	if f.block != nil {
		cur := atomic.AddInt32(&f.inFlight, 1)
		for {
			old := atomic.LoadInt32(&f.maxInFlight)
			if cur <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&f.maxInFlight, old, cur) {
				break
			}
		}
		<-f.block
		atomic.AddInt32(&f.inFlight, -1)
	}

	if err, ok := f.errs[environment]; ok {
		return release.EnvState{}, err
	}
	return f.byEnv[environment], nil
}

type fakeJira struct {
	tasks []jira.ReleaseTask
	err   error
}

func (f fakeJira) Fetch(context.Context) ([]jira.ReleaseTask, error) { return f.tasks, f.err }

type memStore struct {
	mu   sync.Mutex
	data map[string]time.Time
}

func newMemStore() *memStore { return &memStore{data: map[string]time.Time{}} }

func (s *memStore) Get(environment string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data[environment]
	return t, ok
}

func (s *memStore) Set(environment string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[environment] = at
}

// --- вспомогательное ---

var releaseCfg = release.Config{
	MinorOffset:  39,
	CoreServices: []string{"redo-nuxeo", "redo-backend", "redo-camunda", "redo-front", "redo-integration"},
}

func coreState(env string, minor, patch int) release.EnvState {
	st := release.EnvState{Environment: env, CollectedAt: time.Now()}
	for _, svc := range releaseCfg.CoreServices {
		raw := svc + ":main-1." + itoa(minor) + "." + itoa(patch)
		tg, err := tag.ParseImage(raw)
		if err != nil {
			panic(err)
		}
		st.Tags = append(st.Tags, tg)
	}
	return st
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// --- тесты ---

func TestRunWritesComputedReleases(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{
		{Number: 2, Name: "prod-a", Status: "active"},
	}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{
		"prod-a": coreState("prod-a", 29, 0),
	}}

	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 2, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reg.calls != 1 {
		t.Fatalf("Update вызван %d раз, хочу 1", reg.calls)
	}
	if len(reg.updates) != 1 || reg.updates[0].Value != "Release 68" {
		t.Fatalf("updates = %+v, хочу Release 68", reg.updates)
	}
}

func TestRunOneEnvironmentFailureDoesNotStopOthers(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{
		{Number: 2, Name: "broken", Status: "active"},
		{Number: 3, Name: "ok", Status: "active"},
	}}
	coll := &fakeCollector{
		byEnv: map[string]release.EnvState{"ok": coreState("ok", 29, 0)},
		errs:  map[string]error{"broken": errors.New("пайплайн упал")},
	}

	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 2, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v (сбой одной среды не должен ронять прогон)", err)
	}
	if len(reg.updates) != 1 || reg.updates[0].Row.Name != "ok" {
		t.Fatalf("updates = %+v, хочу только ok", reg.updates)
	}
}

func TestRunCollectErrorDoesNotWriteThatRow(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{
		{Number: 2, Name: "broken", Status: "active"},
		{Number: 3, Name: "ok", Status: "active"},
	}}
	coll := &fakeCollector{
		byEnv: map[string]release.EnvState{"ok": coreState("ok", 29, 0)},
		errs:  map[string]error{"broken": errors.New("нет доступа")},
	}

	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, u := range reg.updates {
		if u.Row.Name == "broken" {
			t.Fatalf("updates = %+v: при ошибке сбора строка broken не пишется", reg.updates)
		}
	}
}

func TestRunFailsWhenEveryCollectionFailed(t *testing.T) {
	// Протухший токен GitLab роняет сбор на всех средах. Прогон без единого
	// результата обязан завершиться ошибкой, иначе cron сочтёт его успешным.
	reg := &fakeRegistry{rows: []registry.Row{
		{Number: 2, Name: "prod-a", Status: "active"},
		{Number: 3, Name: "prod-b", Status: "active"},
	}}
	unauthorized := errors.New("GitLab ответил HTTP 401")
	coll := &fakeCollector{errs: map[string]error{"prod-a": unauthorized, "prod-b": unauthorized}}

	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 2, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	err := a.Run(t.Context())
	if !errors.Is(err, unauthorized) {
		t.Fatalf("Run = %v, хочу ошибку с причиной сбоя сбора", err)
	}
	if reg.calls != 0 {
		t.Fatalf("Update вызван %d раз, хочу 0: записывать нечего", reg.calls)
	}
}

func TestRunIntervalSkipsDoNotCountAsFailures(t *testing.T) {
	// Все среды пропущены по интервалу: сбоя не было, прогон успешен.
	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "nit-adilet", Status: "configuring"}}}
	ov := overridesWith(t, map[string]string{"nit-adilet": "interval"})
	store := newMemStore()
	store.Set("nit-adilet", time.Now())

	a := New(reg, &fakeCollector{}, fakeJira{}, ov, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)
	a.store = store

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunDryRunDoesNotCallUpdate(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "prod-a", Status: "active"}}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{"prod-a": coreState("prod-a", 29, 0)}}

	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: false}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reg.calls != 0 {
		t.Fatalf("Update вызван %d раз, хочу 0 (dry-run)", reg.calls)
	}
}

func TestRunSkipsEnvironmentsMarkedSkipInOverrides(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{
		{Number: 2, Name: "dev", Status: "active"},
		{Number: 3, Name: "prod-a", Status: "active"},
	}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{"prod-a": coreState("prod-a", 29, 0)}}
	ov := overridesWith(t, map[string]string{"dev": "skip"})

	a := New(reg, coll, fakeJira{}, ov, Config{Concurrency: 2, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, c := range coll.calls {
		if c == "dev" {
			t.Fatal("dev помечена skip в overrides, но Collect всё равно вызван")
		}
	}
	if len(reg.updates) != 1 || reg.updates[0].Row.Name != "prod-a" {
		t.Fatalf("updates = %+v, хочу только prod-a", reg.updates)
	}
}

func TestRunUsesOverrideEnvironmentNameForCollect(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "sk-prod", Status: "active"}}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{"sk-prod-real-ns": coreState("sk-prod-real-ns", 29, 0)}}
	ov := overridesWith(t, map[string]string{"sk-prod": "environment"})

	a := New(reg, coll, fakeJira{}, ov, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(coll.calls) != 1 || coll.calls[0] != "sk-prod-real-ns" {
		t.Fatalf("Collect вызван с %v, хочу [sk-prod-real-ns]", coll.calls)
	}
	// Запись в реестр всё равно идёт под системным именем из реестра.
	if len(reg.updates) != 1 || reg.updates[0].Row.Name != "sk-prod" {
		t.Fatalf("updates = %+v, хочу строку sk-prod", reg.updates)
	}
}

func TestRunOnlyFlagFiltersToSingleEnvironment(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{
		{Number: 2, Name: "prod-a", Status: "active"},
		{Number: 3, Name: "prod-b", Status: "active"},
	}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{
		"prod-a": coreState("prod-a", 29, 0),
		"prod-b": coreState("prod-b", 29, 0),
	}}

	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 2, PipelineTimeout: time.Second, Release: releaseCfg, Write: true, Only: "prod-b"}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(reg.updates) != 1 || reg.updates[0].Row.Name != "prod-b" {
		t.Fatalf("updates = %+v, хочу только prod-b", reg.updates)
	}
}

func TestRunRespectsIntervalOverride(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "nit-adilet", Status: "configuring"}}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{"nit-adilet": coreState("nit-adilet", 29, 0)}}
	ov := overridesWith(t, map[string]string{"nit-adilet": "interval"})

	store := newMemStore()
	store.Set("nit-adilet", time.Now().Add(-time.Minute)) // интервал 24h, минута назад — рано

	a := New(reg, coll, fakeJira{}, ov, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)
	a.store = store

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(coll.calls) != 0 {
		t.Fatalf("Collect вызван %v, интервал ещё не прошёл", coll.calls)
	}
	if len(reg.updates) != 0 {
		t.Fatalf("updates = %+v, хочу пусто", reg.updates)
	}
}

func TestRunCollectsAfterIntervalElapsed(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "nit-adilet", Status: "configuring"}}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{"nit-adilet": coreState("nit-adilet", 29, 0)}}
	ov := overridesWith(t, map[string]string{"nit-adilet": "interval"})

	store := newMemStore()
	store.Set("nit-adilet", time.Now().Add(-25*time.Hour)) // интервал 24h, прошло больше

	a := New(reg, coll, fakeJira{}, ov, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)
	a.store = store

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(coll.calls) != 1 {
		t.Fatalf("Collect вызван %v, хочу один раз — интервал прошёл", coll.calls)
	}
}

func TestRunWorkerPoolRespectsConcurrency(t *testing.T) {
	block := make(chan struct{})
	rows := make([]registry.Row, 0, 5)
	states := map[string]release.EnvState{}
	for i := 0; i < 5; i++ {
		name := "env" + itoa(i)
		rows = append(rows, registry.Row{Number: i + 2, Name: name, Status: "active"})
		states[name] = coreState(name, 29, 0)
	}
	reg := &fakeRegistry{rows: rows}
	coll := &fakeCollector{byEnv: states, block: block}

	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 2, PipelineTimeout: time.Minute, Release: releaseCfg, Write: true}, nil)

	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()

	// Даём воркерам время выйти на потолок конкурентности, потом отпускаем всех разом.
	time.Sleep(100 * time.Millisecond)
	close(block)

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if max := atomic.LoadInt32(&coll.maxInFlight); max > 2 {
		t.Errorf("одновременно в работе было %d сред, хочу не больше 2 (CONCURRENCY)", max)
	}
}

func TestRunAppliesJiraBaselineForHFAndMismatch(t *testing.T) {
	baseTags := map[string]tag.Tag{}
	for _, svc := range releaseCfg.CoreServices {
		tg, _ := tag.ParseImage(svc + ":main-1.29.0")
		baseTags[svc] = tg
	}
	tasks := []jira.ReleaseTask{{
		Key:     "DOPS-1",
		Number:  68,
		EnvTags: map[string]map[string]tag.Tag{"": baseTags},
	}}

	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "prod-a", Status: "active"}}}
	// patch 13 > базовый patch 0 -> HasHF.
	coll := &fakeCollector{byEnv: map[string]release.EnvState{"prod-a": coreState("prod-a", 29, 13)}}

	a := New(reg, coll, fakeJira{tasks: tasks}, config.Overrides{}, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(reg.updates) != 1 || reg.updates[0].Value != "Release 68 + HF" {
		t.Fatalf("updates = %+v, хочу \"Release 68 + HF\"", reg.updates)
	}
}

func TestRunUndefinedReleaseIsNotWritten(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "office", Status: "active"}}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{
		"office": {Environment: "office", CollectedAt: time.Now()}, // без кор-сервисов
	}}

	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(reg.updates) != 0 {
		t.Fatalf("updates = %+v, хочу пусто — релиз не определён", reg.updates)
	}
}

func TestRunPropagatesRegistryListError(t *testing.T) {
	reg := &fakeRegistry{listErr: errors.New("sheets недоступен")}
	a := New(reg, &fakeCollector{}, fakeJira{}, config.Overrides{}, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err == nil {
		t.Fatal("ошибка List не пробросилась из Run")
	}
}

func TestRunPropagatesJiraFetchError(t *testing.T) {
	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "prod-a", Status: "active"}}}
	a := New(reg, &fakeCollector{}, fakeJira{err: errors.New("jira недоступна")}, config.Overrides{}, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, nil)

	if err := a.Run(t.Context()); err == nil {
		t.Fatal("ошибка Fetch не пробросилась из Run")
	}
}

// overridesWith собирает config.Overrides с одним правилом на среду,
// используя реальный LoadOverrides — чтобы не дублировать формат файла.
func overridesWith(t *testing.T, envKind map[string]string) config.Overrides {
	t.Helper()
	body := "envs:\n"
	for env, kind := range envKind {
		switch kind {
		case "skip":
			body += "  " + env + ":\n    skip: \"тестовая причина\"\n"
		case "environment":
			body += "  " + env + ":\n    environment: \"" + env + "-real-ns\"\n"
		case "interval":
			body += "  " + env + ":\n    interval: \"24h\"\n"
		}
	}
	path := t.TempDir() + "/overrides.yaml"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ov, err := config.LoadOverrides(path)
	if err != nil {
		t.Fatalf("LoadOverrides: %v", err)
	}
	return ov
}

func TestRunMismatchWarningDoesNotRepeatDetails(t *testing.T) {
	// Детали уже выведены в INFO «релиз определён». Повторять их в WARN —
	// два экрана одного и того же: у kpo-prod в списке два десятка пунктов.
	st := coreState("prod-a", 29, 0)
	st.Unparsed = []string{"redo-front:prod-a-164897"}
	versioned := st.Tags[:0]
	for _, tg := range st.Tags {
		if tg.Service != "redo-front" {
			versioned = append(versioned, tg)
		}
	}
	st.Tags = versioned

	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "prod-a", Status: "active"}}}
	coll := &fakeCollector{byEnv: map[string]release.EnvState{"prod-a": st}}

	log, logs := logtest.New()
	a := New(reg, coll, fakeJira{}, config.Overrides{}, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true}, log)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	info, ok := logs.Find("релиз определён")
	if !ok {
		t.Fatal("нет записи о вычисленном релизе")
	}
	if info.Attrs["details"] == "" {
		t.Fatal("details должны остаться в INFO")
	}

	warn, ok := logs.Find("расхождение версий")
	if !ok {
		t.Fatal("нет предупреждения о расхождении")
	}
	if _, repeated := warn.Attrs["details"]; repeated {
		t.Errorf("WARN повторяет details: %v", warn.Attrs)
	}
	if warn.Attrs["core_unversioned"] != "true" {
		t.Errorf("WARN не называет причину расхождения: %v", warn.Attrs)
	}
}

func TestNewUsesStoreFromConfig(t *testing.T) {
	// Хранилище приходит снаружи: файлов app не читает, а без внешнего
	// хранилища interval не переживает завершение процесса.
	reg := &fakeRegistry{rows: []registry.Row{{Number: 2, Name: "nit-adilet", Status: "configuring"}}}
	ov := overridesWith(t, map[string]string{"nit-adilet": "interval"})
	store := newMemStore()
	store.Set("nit-adilet", time.Now())
	coll := &fakeCollector{byEnv: map[string]release.EnvState{"nit-adilet": coreState("nit-adilet", 29, 0)}}

	a := New(reg, coll, fakeJira{}, ov, Config{Concurrency: 1, PipelineTimeout: time.Second, Release: releaseCfg, Write: true, Store: store}, nil)

	if err := a.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(coll.calls) != 0 {
		t.Errorf("сборщик вызван %v: интервал из переданного хранилища не сработал", coll.calls)
	}
}
