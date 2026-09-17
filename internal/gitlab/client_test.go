package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientCollect(t *testing.T) {
	for _, scenario := range []string{"success", "trace", "failed", "empty", "wrong-environment", "malformed", "forbidden", "missing-job", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			polls, posts, traces := 0, 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
					t.Error("missing authentication")
				}
				switch r.URL.Path {
				case "/api/v4/projects/123/pipeline":
					if r.Method != "POST" {
						t.Error("expected POST")
					}
					posts++
					var body struct {
						Ref       string                        `json:"ref"`
						Variables []struct{ Key, Value string } `json:"variables"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.Ref != "main" || len(body.Variables) != 2 || body.Variables[0].Key != "ENVIRONMENT" || body.Variables[0].Value != "demo" || body.Variables[1].Value != "hotfix" {
						t.Errorf("unexpected body: %+v", body)
					}
					w.WriteHeader(201)
					fmt.Fprint(w, `{"id":7}`)
				case "/api/v4/projects/123/pipelines/7":
					polls++
					status := "success"
					if scenario == "failed" {
						status = "failed"
					} else if polls == 1 || scenario == "timeout" {
						status = "running"
					}
					fmt.Fprintf(w, `{"id":7,"status":%q}`, status)
				case "/api/v4/projects/123/pipelines/7/jobs":
					if scenario == "missing-job" {
						fmt.Fprint(w, `[]`)
						return
					}
					if r.URL.Query().Get("page") == "1" {
						w.Header().Set("X-Next-Page", "2")
						fmt.Fprint(w, `[{"id":9,"name":"other","status":"success"}]`)
						return
					}
					fmt.Fprint(w, `[{"id":11,"name":"collect-images","status":"success"}]`)
				case "/api/v4/projects/123/jobs/11/artifacts/images.json":
					if scenario == "trace" {
						w.WriteHeader(404)
						return
					}
					if scenario == "forbidden" {
						w.WriteHeader(403)
						fmt.Fprint(w, "secret response must not be logged")
						return
					}
					if scenario == "malformed" {
						fmt.Fprint(w, "not json")
						return
					}
					images := []string{"redo-backend:main-1.29.13"}
					if scenario == "empty" {
						images = nil
					}
					env := "demo"
					if scenario == "wrong-environment" {
						env = "other"
					}
					json.NewEncoder(w).Encode(artifact{Environment: env, Namespace: "demo", Images: images, CollectedAt: time.Now()})
				case "/api/v4/projects/123/jobs/11/trace":
					traces++
					fmt.Fprintln(w, "setup log\nNamespace: demo\n[\"redo-backend:main-1.29.13\"]\ncleanup log")
				default:
					t.Errorf("unexpected endpoint %s", r.URL)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			client, err := New(Config{URL: srv.URL, Token: "test-token", ProjectID: 123, Ref: "main", PollInterval: time.Millisecond, Scenario: "hotfix"}, srv.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if scenario == "timeout" {
				client.cfg.PollInterval = time.Hour
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
				defer stop()
			}
			state, err := client.Collect(ctx, "demo")
			if scenario == "success" || scenario == "trace" {
				if err != nil || len(state.Tags) != 1 || state.Tags[0].Version.Patch != 13 {
					t.Fatalf("state=%+v err=%v", state, err)
				}
			} else if err == nil {
				t.Fatal("expected error")
			}
			if scenario == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline, got %v", err)
			}
			if posts != 1 {
				t.Errorf("created %d pipelines", posts)
			}
			if (scenario == "trace") != (traces == 1) {
				t.Errorf("unexpected fallback calls: %d", traces)
			}
			if err != nil && strings.Contains(err.Error(), "secret response") {
				t.Fatal("response body exposed")
			}
		})
	}
}

func TestClientDoesNotForwardTokenToArtifactStorage(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("token forwarded to storage")
		}
		fmt.Fprint(w, "artifact")
	}))
	defer storage.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, storage.URL, http.StatusFound) }))
	defer api.Close()
	c, err := New(Config{URL: api.URL, Token: "test-token", ProjectID: 1, Ref: "main", PollInterval: time.Second}, api.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := c.request(t.Context(), http.MethodGet, "/jobs/1/artifacts/images.json", nil)
	if err != nil || string(data) != "artifact" {
		t.Fatalf("data=%s err=%v", data, err)
	}
}

func TestTraceRejectsMissingAndEmptyResults(t *testing.T) {
	for _, input := range []string{"no namespace", "Namespace: demo", "Namespace: demo\n(no images found)", "Namespace: demo\n[]", "Namespace: demo\nnot json"} {
		if _, err := parseTrace([]byte(input), "demo"); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestTraceWithRunnerTimestampsAndANSI(t *testing.T) {
	input := "2026-09-16T10:22:56.514628Z 01O \x1b[32mNamespace: demo\x1b[0m\n" +
		"2026-09-16T10:22:56.514633Z 01O [\"redo-backend:main-1.29.13\"]\n" +
		"2026-09-16T10:22:56.695551Z 00O section_end:1789554176:step_script\r\x1b[0K\n"
	state, err := parseTrace([]byte(input), "demo")
	if err != nil || state.Namespace != "demo" || len(state.Tags) != 1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	_, err = parseTrace([]byte("2026-09-16T10:22:56Z 01O Namespace: demo\n2026-09-16T10:22:56Z 01O (no images found)\n"), "demo")
	if err == nil {
		t.Fatal("accepted empty namespace")
	}
}

func TestRunnerOverrideSentToPipeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Variables []struct{ Key, Value string } }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		found := false
		for _, v := range body.Variables {
			if v.Key == "tags_name" && v.Value == "runner-a" {
				found = true
			}
		}
		if !found {
			t.Errorf("runner not sent: %+v", body)
		}
		w.WriteHeader(400)
	}))
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Token: "fixture", ProjectID: 1, Ref: "main", PollInterval: time.Second}, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.CollectWithRunner(t.Context(), "prod-a", "runner-a"); err == nil {
		t.Fatal("expected server rejection")
	}
}
