package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"env-release-tracker/internal/config"
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
