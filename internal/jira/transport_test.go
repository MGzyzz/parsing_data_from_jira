package jira

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// counter считает, сколько запросов реально ушло дальше по цепочке.
type counter struct {
	calls int
	next  http.RoundTripper
}

func (c *counter) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls++
	return c.next.RoundTrip(r)
}

func TestGuardAllowsReadCalls(t *testing.T) {
	// POST /search — читающий вызов, несмотря на метод: JQL слишком длинный
	// для query-строки. Поэтому правило "только GET" не годится.
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/rest/api/2/field"},
		{http.MethodGet, "/rest/api/2/issue/DOPS-4820"},
		{http.MethodPost, "/rest/api/2/search"},
	}

	for _, c := range allowed {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			cnt := &counter{next: http.DefaultTransport}
			client := &http.Client{Transport: ReadOnly(cnt)}

			req, err := http.NewRequest(c.method, srv.URL+c.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("читающий вызов отклонён: %v", err)
			}
			resp.Body.Close()

			if cnt.calls != 1 {
				t.Errorf("запросов ушло %d, хочу 1", cnt.calls)
			}
		})
	}
}

func TestGuardBlocksMutatingCalls(t *testing.T) {
	// Ни один из этих вызовов не должен дойти до сети.
	blocked := []struct{ method, path string }{
		{http.MethodDelete, "/rest/api/2/issue/DOPS-4820"},
		{http.MethodPut, "/rest/api/2/issue/DOPS-4820"},
		{http.MethodPatch, "/rest/api/2/issue/DOPS-4820"},
		{http.MethodPost, "/rest/api/2/issue"},
		{http.MethodPost, "/rest/api/2/issue/DOPS-4820/comment"},
		{http.MethodPost, "/rest/api/2/issue/DOPS-4820/transitions"},
		{http.MethodPost, "/rest/api/2/version"},
		{http.MethodGet, "/rest/api/2/mypermissions"},
	}

	for _, c := range blocked {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("запрещённый вызов %s %s дошёл до сервера", c.method, c.path)
			}))
			defer srv.Close()

			cnt := &counter{next: http.DefaultTransport}
			client := &http.Client{Transport: ReadOnly(cnt)}

			req, err := http.NewRequest(c.method, srv.URL+c.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				t.Fatalf("%s %s прошёл без ошибки", c.method, c.path)
			}
			if !strings.Contains(err.Error(), "запрещённый вызов") {
				t.Errorf("ошибка %q не объясняет причину отказа", err)
			}
			if cnt.calls != 0 {
				t.Errorf("запрещённый вызов ушёл в сеть: запросов %d, хочу 0", cnt.calls)
			}
		})
	}
}

func TestGuardIgnoresQueryString(t *testing.T) {
	// Разрешение даёт путь, а не строка запроса: иначе достаточно было бы
	// дописать параметр, чтобы обойти белый список.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("запрещённый вызов дошёл до сервера")
	}))
	defer srv.Close()

	client := &http.Client{Transport: ReadOnly(http.DefaultTransport)}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/rest/api/2/issue/X?expand=names", nil)

	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("DELETE со строкой запроса прошёл")
	}
}
