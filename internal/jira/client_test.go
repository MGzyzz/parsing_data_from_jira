package jira

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// issueStub — минимальный набор полей ответа /rest/api/2/search.
type issueStub struct {
	key         string
	summary     string
	description string
	created     string
}

func searchServer(t *testing.T, pages [][]issueStub, total int) *httptest.Server {
	t.Helper()
	var calls int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/2/search" {
			t.Fatalf("неожиданный запрос: %s %s", r.Method, r.URL.Path)
		}
		if calls >= len(pages) {
			t.Fatalf("лишний запрос страницы: %d", calls)
		}
		page := pages[calls]
		calls++

		var body struct {
			JQL     string `json:"jql"`
			StartAt int    `json:"startAt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("тело запроса: %v", err)
		}
		if body.JQL != testJQL {
			t.Errorf("jql = %q, хочу %q", body.JQL, testJQL)
		}

		resp := map[string]any{
			"startAt": body.StartAt,
			"total":   total,
			"issues":  []map[string]any{},
		}
		issues := make([]map[string]any, 0, len(page))
		for _, iss := range page {
			issues = append(issues, map[string]any{
				"key": iss.key,
				"fields": map[string]any{
					"summary":     iss.summary,
					"description": iss.description,
					"created":     iss.created,
					"status":      map[string]any{"statusCategory": map[string]string{"key": "done"}},
				},
			})
		}
		resp["issues"] = issues

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

const testJQL = `summary ~ "Release" AND project = DevOps`

func TestFetchParsesReleaseIssues(t *testing.T) {
	srv := searchServer(t, [][]issueStub{{
		{key: "DOPS-1", summary: "Release 68", description: "*Projects:*\n # nuxeo: main-1.29.0", created: "2026-07-20T10:00:00.000+0500"},
		{key: "DOPS-2", summary: "Не релиз, просто release упомянут в тексте", created: "2026-07-20T10:00:00.000+0500"},
	}}, 2)
	defer srv.Close()

	c := New(Config{URL: srv.URL, JQL: testJQL}, srv.Client())

	tasks, err := c.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("задач = %d, хочу 1 (DOPS-2 без номера и тегов должна отсеяться)", len(tasks))
	}
	if tasks[0].Key != "DOPS-1" || tasks[0].Number != 68 {
		t.Errorf("задача = %+v", tasks[0])
	}
}

func TestFetchPaginates(t *testing.T) {
	page1 := []issueStub{{key: "DOPS-1", summary: "Release 1", description: "*Projects:*\n # nuxeo: main-1.0.0", created: "2026-07-20T10:00:00.000+0500"}}
	page2 := []issueStub{{key: "DOPS-2", summary: "Release 2", description: "*Projects:*\n # nuxeo: main-2.0.0", created: "2026-07-20T10:00:00.000+0500"}}
	srv := searchServer(t, [][]issueStub{page1, page2}, 2)
	defer srv.Close()

	// maxResults на сервере не подделать без своей реализации пагинации, так
	// что искусственно уменьшаем размер страницы через конфиг клиента.
	c := New(Config{URL: srv.URL, JQL: testJQL}, srv.Client())
	c.pageSize = 1

	tasks, err := c.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("задач = %d, хочу 2 (обе страницы)", len(tasks))
	}
}

func TestFetchRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "Client must be authenticated")
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, JQL: testJQL}, srv.Client())

	_, err := c.Fetch(t.Context())
	if err == nil {
		t.Fatal("ошибка не вернулась")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("ошибка %q не называет код статуса", err)
	}
}

func TestFetchParsesCreatedDate(t *testing.T) {
	srv := searchServer(t, [][]issueStub{{
		{key: "DOPS-1", summary: "Release 68", description: "*Projects:*\n # nuxeo: main-1.29.0", created: "2026-07-20T10:00:00.000+0500"},
	}}, 1)
	defer srv.Close()

	c := New(Config{URL: srv.URL, JQL: testJQL}, srv.Client())

	tasks, err := c.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if tasks[0].Date.IsZero() {
		t.Error("Date не разобрана из поля created")
	}
}
