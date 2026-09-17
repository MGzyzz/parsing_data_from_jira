package jira

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Разрешённые операции Jira API задаются парой HTTP-метод и путь.
// POST /search выполняет поиск и не изменяет данные.
var (
	exactReadOnly = map[string]bool{
		"GET /rest/api/2/field":   true,
		"POST /rest/api/2/search": true,
	}

	// prefixReadOnly разрешает GET-запрос отдельной задачи без вложенных путей.
	prefixReadOnly = []string{
		"GET /rest/api/2/issue/",
	}
)

// readOnlyGuard отклоняет всё, чего нет в белом списке, до отправки запроса.
type readOnlyGuard struct {
	next http.RoundTripper
}

// ReadOnly отклоняет запросы, отсутствующие в списке разрешённых операций.
func ReadOnly(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &readOnlyGuard{next: next}
}

func (g *readOnlyGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	// Параметры запроса не участвуют в проверке разрешённого пути.
	key := r.Method + " " + r.URL.Path

	if !allowed(key) {
		return nil, fmt.Errorf("запрещённый вызов: %s %s (Jira только на чтение)", r.Method, r.URL.Path)
	}
	return g.next.RoundTrip(r)
}

// bearerAuth передаёт токен только настроенному origin Jira.
type bearerAuth struct {
	token  string
	origin *url.URL
	next   http.RoundTripper
}

// Authenticated оборачивает транспорт токеном Jira PAT.
//
// Клонируем запрос перед правкой: RoundTripper не должен менять r
// у вызывающего, а исходный *http.Request может быть переиспользован
// (например, при ретраях выше по стеку).
func Authenticated(baseURL, token string, next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	origin, _ := url.Parse(baseURL)
	return &bearerAuth{token: token, origin: origin, next: next}
}

func (a *bearerAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	if a.origin == nil || a.origin.Host == "" || a.origin.User != nil ||
		(a.origin.Scheme != "https" && a.origin.Scheme != "http") ||
		r.URL.Scheme != a.origin.Scheme || !strings.EqualFold(r.URL.Host, a.origin.Host) || r.URL.User != nil {
		return nil, fmt.Errorf("Jira: запрос вне настроенного origin запрещён")
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return a.next.RoundTrip(r)
}

func allowed(key string) bool {
	if exactReadOnly[key] {
		return true
	}
	for _, p := range prefixReadOnly {
		// После ключа задачи не допускаются дополнительные сегменты пути.
		if rest, found := strings.CutPrefix(key, p); found && rest != "" && !strings.Contains(rest, "/") {
			return true
		}
	}
	return false
}
