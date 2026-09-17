package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAuthBlocksRedirectAndDowngrade(t *testing.T) {
	for _, target := range []string{"https://other.example/rest/api/2/search", "https://jira.example:444/rest/api/2/search", "http://jira.example/rest/api/2/search"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			next := transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					t.Fatal("redirect reached network")
				}
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Fatal("missing auth")
				}
				return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})
			c := &http.Client{Transport: Authenticated("https://jira.example", "fixture", ReadOnly(next))}
			req, _ := http.NewRequest("POST", "https://jira.example/rest/api/2/search", strings.NewReader(`{}`))
			if resp, err := c.Do(req); err == nil {
				resp.Body.Close()
				t.Fatal("redirect accepted")
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if req.Header.Get("Authorization") != "" {
				t.Fatal("original request mutated")
			}
		})
	}
}

func TestMultilineEnvironmentBlocksStayIsolated(t *testing.T) {
	description := "Projects:\n# backend: main-1.29.0\nEnvironments:\n# prod-a\n# prod-b\nProjects:\n# backend: release-1.29.1\nEnvironments:\nProjects:\n# backend: main-1.29.99"
	task, ok := ParseIssue("PROJ-1", "Release 68", description, time.Now())
	if !ok {
		t.Fatal("not parsed")
	}
	for _, env := range []string{"prod-a", "prod-b"} {
		if task.TagsFor(env)["redo-backend"].Version.Patch != 1 {
			t.Fatalf("wrong %s baseline", env)
		}
	}
	if task.TagsFor("prod-c")["redo-backend"].Version.Patch != 0 {
		t.Fatal("main flow overwritten")
	}
	if len(task.Skipped) == 0 {
		t.Fatal("empty environment block not diagnosed")
	}
}

func TestFetchSkipsBadIssuesAndRequestsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Fields []string `json:"fields"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !strings.Contains(strings.Join(req.Fields, ","), "status") {
			t.Error("status not requested")
		}
		io.WriteString(w, `{"total":3,"issues":[
   {"key":"PROJ-1","fields":{"summary":"Release 68","description":"Projects:\n# backend: main-1.29.0","created":"bad","status":{"statusCategory":{"key":"done"}}}},
   {"key":"PROJ-2","fields":{"summary":"Release 68","description":"Projects:\n# backend: main-1.29.0","created":"2026-07-20T10:00:00.000+0500","status":{"statusCategory":{"key":"new"}}}},
   {"key":"PROJ-3","fields":{"summary":"Release 68","description":"Projects:\n# backend: main-1.29.0","created":"2026-07-20T10:00:00.000+0500","status":{"name":"ГОТОВО","statusCategory":{"key":"done"}}}}
  ]}`)
	}))
	defer srv.Close()
	tasks, err := New(Config{URL: srv.URL}, srv.Client()).Fetch(t.Context())
	if err != nil || len(tasks) != 1 || tasks[0].Key != "PROJ-3" {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
}

func TestFetchDeadlineBoundsWaiting(t *testing.T) {
	c := New(Config{URL: "https://jira.example", FetchTimeout: 10 * time.Millisecond}, &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })})
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestHTTPErrorDoesNotExposeBody(t *testing.T) {
	c := New(Config{URL: "https://jira.example"}, &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("private-value"))}, nil
	})})
	_, err := c.Fetch(t.Context())
	if err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestEnvironmentListsIgnoreProseAndAcceptBullets(t *testing.T) {
	task, ok := ParseIssue("PROJ-1", "Release 68", "Environments:\n- prod-a\n* prod-b\nКомментарий к списку сред\nProjects:\n# backend: main-1.29.0", time.Now())
	if !ok || len(task.EnvTags) != 2 || len(task.TagsFor("prod-a")) != 1 || len(task.TagsFor("prod-b")) != 1 {
		t.Fatalf("task=%+v", task)
	}
}
