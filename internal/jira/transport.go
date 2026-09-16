package jira

import (
	"fmt"
	"net/http"
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

// bearerAuth проставляет токен во все запросы, ушедшие дальше по цепочке.
type bearerAuth struct {
	token string
	next  http.RoundTripper
}

// Authenticated оборачивает транспорт токеном Jira PAT.
//
// Клонируем запрос перед правкой: RoundTripper не должен менять r
// у вызывающего, а исходный *http.Request может быть переиспользован
// (например, при ретраях выше по стеку).
func Authenticated(token string, next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &bearerAuth{token: token, next: next}
}

func (a *bearerAuth) RoundTrip(r *http.Request) (*http.Response, error) {
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
