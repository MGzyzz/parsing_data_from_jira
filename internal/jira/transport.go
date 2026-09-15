package jira

import (
	"fmt"
	"net/http"
	"strings"
)

// Правило «Jira только на чтение» держится этим списком, а не дисциплиной.
//
// По методу разграничить нельзя: POST /search — читающий вызов, потому что
// JQL не помещается в query-строку, а GET /mypermissions читающий, но нам
// не нужен. Поэтому разрешение даёт конкретный эндпоинт.
var (
	exactReadOnly = map[string]bool{
		"GET /rest/api/2/field":   true,
		"POST /rest/api/2/search": true,
	}

	// prefixReadOnly покрывает пути с ключом задачи: /rest/api/2/issue/DOPS-4820.
	// Только GET и только сама задача: подпути вроде /comment и /transitions
	// изменяют данные и сюда не попадают.
	prefixReadOnly = []string{
		"GET /rest/api/2/issue/",
	}
)

// readOnlyGuard отклоняет всё, чего нет в белом списке, до отправки запроса.
type readOnlyGuard struct {
	next http.RoundTripper
}

// ReadOnly оборачивает транспорт запретом на изменяющие вызовы.
//
// Guard стоит на самом низком слое: любой запрос клиента проходит через него,
// и мутирующий вызов, дописанный выше по стеку, физически не уйдёт в сеть.
func ReadOnly(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &readOnlyGuard{next: next}
}

func (g *readOnlyGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	// Сравниваем путь без строки запроса: иначе белый список обходился бы
	// дописыванием параметра.
	key := r.Method + " " + r.URL.Path

	if !allowed(key) {
		return nil, fmt.Errorf("запрещённый вызов: %s %s (Jira только на чтение)", r.Method, r.URL.Path)
	}
	return g.next.RoundTrip(r)
}

func allowed(key string) bool {
	if exactReadOnly[key] {
		return true
	}
	for _, p := range prefixReadOnly {
		// Подпуть задачи (/comment, /transitions) изменяет данные,
		// поэтому после ключа не должно быть новых сегментов.
		if rest, found := strings.CutPrefix(key, p); found && rest != "" && !strings.Contains(rest, "/") {
			return true
		}
	}
	return false
}
