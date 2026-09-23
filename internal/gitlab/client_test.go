package gitlab

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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
				case "/api/v4/projects/123/jobs/11/artifacts":
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
						w.Write(zipWith(t, map[string][]byte{"demo-22-09-2026-17" + artifactSuffix: []byte("not json")}))
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
					w.Write(artifactZip(t, artifact{Environment: env, Namespace: "demo", Images: images, CollectedAt: time.Now()}))
				case "/api/v4/projects/123/pipelines/7/bridges":
					fmt.Fprint(w, `[]`)
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
	data, _, err := c.request(t.Context(), http.MethodGet, "/jobs/1/artifacts", nil)
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

// pipelineTree отвечает как боевой collect-images: джоба лежит не в запущенном
// пайплайне, а в дочернем. chain задаёт цепочку пайплайнов от родителя к джобе.
func pipelineTree(t *testing.T, chain []int, jobID int, artifacts http.HandlerFunc) *httptest.Server {
	t.Helper()
	const base = "/api/v4/projects/123"
	next := map[int]int{}
	for i := 0; i+1 < len(chain); i++ {
		next[chain[i]] = chain[i+1]
	}
	last := chain[len(chain)-1]
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, base)
		if !ok {
			t.Errorf("unexpected endpoint %s", r.URL)
			return
		}
		switch {
		case rest == "/pipeline":
			fmt.Fprintf(w, `{"id":%d}`, chain[0])
		case strings.HasPrefix(rest, "/pipelines/"):
			id, tail, _ := strings.Cut(strings.TrimPrefix(rest, "/pipelines/"), "/")
			n, err := strconv.Atoi(id)
			if err != nil {
				t.Errorf("bad pipeline id in %s", r.URL)
				return
			}
			switch tail {
			case "":
				fmt.Fprintf(w, `{"id":%d,"status":"success"}`, n)
			case "jobs":
				if n == last {
					fmt.Fprintf(w, `[{"id":%d,"name":"collect-images","status":"success"}]`, jobID)
					return
				}
				fmt.Fprint(w, `[{"id":1,"name":"generate-environment-pipeline","status":"success"}]`)
			case "bridges":
				if child, ok := next[n]; ok {
					fmt.Fprintf(w, `[{"name":"collect-images","downstream_pipeline":{"id":%d,"status":"success"}}]`, child)
					return
				}
				fmt.Fprint(w, `[]`)
			default:
				t.Errorf("unexpected endpoint %s", r.URL)
			}
		case strings.HasPrefix(rest, "/jobs/"):
			artifacts(w, r)
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
		}
	}))
}

func TestCollectFindsJobInChildPipeline(t *testing.T) {
	for name, chain := range map[string][]int{
		"дочерний":            {7, 8},
		"через промежуточный": {7, 8, 9},
	} {
		t.Run(name, func(t *testing.T) {
			srv := pipelineTree(t, chain, 11, func(w http.ResponseWriter, _ *http.Request) {
				w.Write(artifactZip(t, artifact{Environment: "demo", Namespace: "demo", Images: []string{"redo-backend:main-1.29.13"}, CollectedAt: time.Now()}))
			})
			defer srv.Close()
			c, err := New(Config{URL: srv.URL, Token: "test-token", ProjectID: 123, Ref: "main", PollInterval: time.Millisecond}, srv.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			state, err := c.Collect(t.Context(), "demo")
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if len(state.Tags) != 1 || state.Tags[0].Version.Patch != 13 {
				t.Fatalf("state=%+v", state)
			}
		})
	}
}

// fanoutServer отвечает деревом, в котором у родителя несколько дочерних
// пайплайнов с джобой collect-images: так выглядит запуск списка сред.
func fanoutServer(t *testing.T, children []int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/123")
		id, tail, _ := strings.Cut(strings.TrimPrefix(rest, "/pipelines/"), "/")
		switch {
		case rest == "/pipeline":
			fmt.Fprint(w, `{"id":7}`)
		case tail == "":
			fmt.Fprintf(w, `{"id":%s,"status":"success"}`, id)
		case tail == "jobs":
			if id == "7" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprintf(w, `[{"id":1%s,"name":"collect-images","status":"success"}]`, id)
		case tail == "bridges":
			if id != "7" {
				fmt.Fprint(w, `[]`)
				return
			}
			parts := make([]string, 0, len(children))
			for _, c := range children {
				parts = append(parts, fmt.Sprintf(`{"name":"collect-images","downstream_pipeline":{"id":%d,"status":"success"}}`, c))
			}
			fmt.Fprintf(w, `[%s]`, strings.Join(parts, ","))
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
		}
	}))
}

// Среду мы всегда запрашиваем одну, поэтому несколько джоб означают, что ответ
// относится не к нашему запуску. Брать первую попавшуюся нельзя: в колонку
// уедет релиз чужой среды.
func TestCollectRejectsSeveralEnvironments(t *testing.T) {
	srv := fanoutServer(t, []int{8, 9})
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Token: "test-token", ProjectID: 123, Ref: "main", PollInterval: time.Millisecond}, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Collect(t.Context(), "demo")
	if err == nil {
		t.Fatal("несколько джоб collect-images приняты за успех")
	}
	if !strings.Contains(err.Error(), "collect-images") {
		t.Fatalf("ошибка не называет причину: %v", err)
	}
}

// Спуск по дочерним пайплайнам ограничен: испорченные или зацикленные данные
// не должны уводить сбор в бесконечную рекурсию.
func TestCollectLimitsPipelineDepth(t *testing.T) {
	srv := pipelineTree(t, []int{7, 8, 9, 10, 11, 12}, 99, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("спуск не остановился и дошёл до артефакта")
	})
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Token: "test-token", ProjectID: 123, Ref: "main", PollInterval: time.Millisecond}, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Collect(t.Context(), "demo"); err == nil {
		t.Fatal("слишком глубокая вложенность принята за успех")
	}
}

// Родитель может отчитаться об успехе раньше, чем доработает дочерний пайплайн:
// strategy: depend задаётся на каждом триггере отдельно, и на вложенных его
// может не быть. Тогда джобу видно, но она ещё идёт.
func TestCollectWaitsForChildPipeline(t *testing.T) {
	childPolls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/123")
		id, tail, _ := strings.Cut(strings.TrimPrefix(rest, "/pipelines/"), "/")
		switch {
		case rest == "/pipeline":
			fmt.Fprint(w, `{"id":7}`)
		case tail == "" && id == "7":
			fmt.Fprint(w, `{"id":7,"status":"success"}`)
		case tail == "" && id == "8":
			childPolls++
			status := "success"
			if childPolls == 1 {
				status = "running"
			}
			fmt.Fprintf(w, `{"id":8,"status":%q}`, status)
		case tail == "jobs" && id == "7":
			fmt.Fprint(w, `[{"id":1,"name":"generate-environment-pipeline","status":"success"}]`)
		case tail == "jobs" && id == "8":
			status := "success"
			if childPolls < 2 {
				status = "running"
			}
			fmt.Fprintf(w, `[{"id":11,"name":"collect-images","status":%q}]`, status)
		case tail == "bridges" && id == "7":
			fmt.Fprint(w, `[{"name":"collect-images","downstream_pipeline":{"id":8,"status":"running"}}]`)
		case tail == "bridges":
			fmt.Fprint(w, `[]`)
		case strings.HasPrefix(rest, "/jobs/11/artifacts"):
			w.Write(artifactZip(t, artifact{Environment: "demo", Namespace: "demo", Images: []string{"redo-backend:main-1.29.13"}, CollectedAt: time.Now()}))
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
		}
	}))
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Token: "test-token", ProjectID: 123, Ref: "main", PollInterval: time.Millisecond}, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := c.Collect(t.Context(), "demo")
	if err != nil {
		t.Fatalf("сбор не дождался дочернего пайплайна: %v", err)
	}
	if len(state.Tags) != 1 {
		t.Fatalf("state=%+v", state)
	}
}

func zipWith(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// artifactZip собирает архив артефактов в том виде, в каком его отдаёт джоба:
// один файл результата, имя которого оканчивается на artifactSuffix.
func artifactZip(t *testing.T, a artifact) []byte {
	t.Helper()
	body, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return zipWith(t, map[string][]byte{a.Environment + "-22-09-2026-17" + artifactSuffix: body})
}

// Боевой collect-images кладёт результат не в images.json, а в файл
// <среда>-<день-месяц-год-час>-collect-images.output. Час считается по UTC+5,
// поэтому имя сопоставляем по маске и никогда не собираем сами.
func TestCollectReadsArtifactArchive(t *testing.T) {
	body, err := json.Marshal(artifact{Environment: "demo", Namespace: "demo", Images: []string{"redo-backend:main-1.29.13"}, CollectedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	archive := zipWith(t, map[string][]byte{"demo-22-09-2026-17-collect-images.output": body})
	srv := pipelineTree(t, []int{7, 8}, 11, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/jobs/11/artifacts") {
			t.Errorf("запрошен неверный путь артефакта: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Write(archive)
	})
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Token: "test-token", ProjectID: 123, Ref: "main", PollInterval: time.Millisecond}, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := c.Collect(t.Context(), "demo")
	if err != nil {
		t.Fatalf("артефакт не прочитан: %v", err)
	}
	if len(state.Tags) != 1 || state.Tags[0].Version.Patch != 13 {
		t.Fatalf("state=%+v", state)
	}
}

// Архив есть, а файла результата в нём нет — это «результата не существует»,
// а не порча данных: читаем лог джобы, как и при полном отсутствии артефактов.
func TestCollectFallsBackToTraceWhenArchiveHasNoResult(t *testing.T) {
	traces := 0
	srv := pipelineTree(t, []int{7, 8}, 11, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/trace") {
			traces++
			fmt.Fprintln(w, "Namespace: demo\n[\"redo-backend:main-1.29.13\"]")
			return
		}
		w.Write(zipWith(t, map[string][]byte{"metadata.gz": []byte("not a result")}))
	})
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, Token: "test-token", ProjectID: 123, Ref: "main", PollInterval: time.Millisecond}, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := c.Collect(t.Context(), "demo")
	if err != nil {
		t.Fatalf("не ушли в trace: %v", err)
	}
	if traces != 1 || len(state.Tags) != 1 {
		t.Fatalf("traces=%d state=%+v", traces, state)
	}
}

// Сжатие скрывает настоящий объём: маленький архив разворачивается в сколько
// угодно. Распаковываем в память, поэтому предел обязателен.
func TestArchiveRejectsOversizedResult(t *testing.T) {
	archive := zipWith(t, map[string][]byte{"demo-22-09-2026-17" + artifactSuffix: make([]byte, 5<<20)})
	if len(archive) > 64<<10 {
		t.Fatalf("архив не сжался, проверка бессмысленна: %d байт", len(archive))
	}
	_, err := artifactFromArchive(archive)
	if err == nil {
		t.Fatal("распакован файл сверх предела")
	}
	if !strings.Contains(err.Error(), "превышает") {
		t.Fatalf("ошибка не про размер: %v", err)
	}
}
