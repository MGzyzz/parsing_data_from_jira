package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// serve отправляет запрос обработчику и падает, если тот не ответил:
// заблокированный обработчик вешает остановку сервера после входа.
func serve(t *testing.T, h http.Handler, target string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		close(done)
	}()
	select {
	case <-done:
		return rec.Code
	case <-time.After(time.Second):
		t.Fatalf("обработчик %s заблокирован", target)
		return 0
	}
}

func TestCallbackDeliversCode(t *testing.T) {
	results := make(chan callbackResult, 1)

	if code := serve(t, callbackHandler("state-1", results), "/?state=state-1&code=abc"); code != http.StatusOK {
		t.Fatalf("HTTP %d, хочу 200", code)
	}
	if res := <-results; res.err != nil || res.code != "abc" {
		t.Fatalf("результат = %+v, хочу код abc", res)
	}
}

func TestCallbackRejectsForeignState(t *testing.T) {
	results := make(chan callbackResult, 1)

	if code := serve(t, callbackHandler("state-1", results), "/?state=other&code=abc"); code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, хочу 400", code)
	}
	if res := <-results; res.err == nil {
		t.Fatal("чужой state принят")
	}
}

func TestCallbackReportsGoogleError(t *testing.T) {
	results := make(chan callbackResult, 1)

	serve(t, callbackHandler("state-1", results), "/?state=state-1&error=access_denied")
	if res := <-results; res.err == nil {
		t.Fatal("отказ в доступе не стал ошибкой")
	}
}

func TestCallbackExtraRequestsDoNotBlock(t *testing.T) {
	// После редиректа браузер запрашивает /favicon.ico, могут прийти и повторы.
	// Никто уже не читает канал — обработчик всё равно обязан ответить.
	results := make(chan callbackResult, 1)
	h := callbackHandler("state-1", results)

	serve(t, h, "/?state=state-1&code=abc")
	if code := serve(t, h, "/favicon.ico"); code != http.StatusNotFound {
		t.Errorf("favicon: HTTP %d, хочу 404", code)
	}
	serve(t, h, "/?state=state-1&code=again")
	serve(t, h, "/?state=other")

	if res := <-results; res.code != "abc" {
		t.Errorf("результат = %+v, хочу первый код abc", res)
	}
}
