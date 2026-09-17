package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"env-release-tracker/internal/config"
	"env-release-tracker/internal/jira"
	"env-release-tracker/internal/registry"
	"env-release-tracker/internal/release"
)

type runnerCollector struct{ env, runner string }

func (c *runnerCollector) Collect(context.Context, string) (release.EnvState, error) {
	panic("runner override lost")
}
func (c *runnerCollector) CollectWithRunner(_ context.Context, env, runner string) (release.EnvState, error) {
	c.env, c.runner = env, runner
	return coreState(env, 29, 0), nil
}

func TestRunnerOverrideReachesCollector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overrides.yaml")
	if err := os.WriteFile(path, []byte("envs:\n  prod-a:\n    environment: cluster-a\n    tags_name: runner-a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	overrides, err := config.LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	collector := &runnerCollector{}
	a := New(&fakeRegistry{}, collector, fakeJira{}, overrides, Config{PipelineTimeout: time.Second, Release: releaseCfg}, nil)
	got := a.collectOne(t.Context(), registry.Row{Name: "prod-a"}, nil)
	if got.err != nil || collector.env != "cluster-a" || collector.runner != "runner-a" || got.result.Release != 68 {
		t.Fatalf("collector=%+v outcome=%+v", collector, got)
	}
}

func TestBaselineIncludesOnlyLaterHotfixesForEnvironment(t *testing.T) {
	date := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	base, _ := jira.ParseIssue("PROJ-1", "Release 68", "Projects:\n# backend: main-1.29.0", date)
	early, _ := jira.ParseIssue("PROJ-2", "HF Release", "Projects:\n# backend: main-1.29.13", date.Add(-time.Hour))
	late, _ := jira.ParseIssue("PROJ-3", "HF Release", "Environments:\n# prod-a\nProjects:\n# backend: main-1.29.13", date.Add(time.Hour))
	tasks := []jira.ReleaseTask{early, base, late}
	got := baselineFor(tasks, 68, "prod-a")
	state := coreState("prod-a", 29, 0)
	state.Tags[1].Version.Patch = 13
	if result := release.Compute(state, got, releaseCfg); result.HasHF != true {
		t.Fatalf("result=%+v", result)
	}
	if len(got.Hotfixes) != 1 || got.Hotfixes[0]["redo-backend"].Version.Patch != 13 {
		t.Fatalf("base=%+v", got)
	}
	other := baselineFor(tasks, 68, "prod-b")
	if len(other.Hotfixes) != 1 || len(other.Hotfixes[0]) != 0 {
		t.Fatal("HF applied to other environment")
	}
}
