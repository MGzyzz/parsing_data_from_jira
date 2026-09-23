package googleauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestLogin(t *testing.T) {
	for _, scenario := range []string{"success", "denied", "missing-refresh", "exchange-failed", "save-failed"} {
		t.Run(scenario, func(t *testing.T) {
			var challenge string
			exchanges := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				exchanges++
				r.ParseForm()
				if r.Form.Get("code") != "code" || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != challenge {
					t.Error("code/PKCE mismatch")
				}
				if scenario == "exchange-failed" {
					http.Error(w, "upstream-private-detail", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if scenario == "missing-refresh" {
					w.Write([]byte(`{"access_token":"access","token_type":"Bearer","expires_in":3600}`))
					return
				}
				w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`))
			}))
			defer endpoint.Close()
			cfg := &oauth2.Config{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://example.test/oauth/callback", Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.example/auth", TokenURL: endpoint.URL, AuthStyle: oauth2.AuthStyleInParams}}
			path := filepath.Join(t.TempDir(), "token.json")
			done := make(chan error, 1)
			h := newHandler(t.Context(), cfg, "setup", true, func(tok *oauth2.Token) error {
				if scenario == "save-failed" {
					return os.ErrPermission
				}
				return Save(path, tok)
			}, done)
			request := func(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", path, nil)
				if cookie != nil {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := request("/oauth/start?key=wrong", nil); w.Code != 403 {
				t.Fatal(w.Code)
			}
			start := request("/oauth/start?key=setup", nil)
			if start.Code != 302 {
				t.Fatal(start.Code)
			}
			location, _ := url.Parse(start.Header().Get("Location"))
			q := location.Query()
			challenge = q.Get("code_challenge")
			if challenge == "" || q.Get("access_type") != "offline" {
				t.Fatal(q)
			}
			cookie := start.Result().Cookies()[0]
			if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatal("unsafe cookie")
			}
			callback := "/oauth/callback?state=" + q.Get("state") + "&code=code"
			if w := request(callback, nil); w.Code != 400 {
				t.Fatal("accepted other browser")
			}
			if w := request("/oauth/callback?state=wrong&code=code", cookie); w.Code != 400 {
				t.Fatal("accepted invalid state")
			}
			if scenario == "denied" {
				callback = "/oauth/callback?state=" + q.Get("state") + "&error=access_denied"
			}
			w := request(callback, cookie)
			err := <-done
			if strings.Contains(w.Body.String(), "upstream-private-detail") || (err != nil && strings.Contains(err.Error(), "upstream-private-detail")) {
				t.Fatal("leaked exchange error")
			}
			if scenario == "success" {
				if err != nil || w.Code != 200 {
					t.Fatalf("%v %d", err, w.Code)
				}
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0600 {
					t.Fatal(info.Mode())
				}
			} else {
				if err == nil || w.Code < 400 {
					t.Fatal("failure accepted")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("saved failed authorization")
				}
			}
			if w := request(callback, cookie); w.Code != 400 {
				t.Fatal("accepted replay")
			}
			if scenario == "denied" && exchanges != 0 {
				t.Fatal("exchanged denied consent")
			}
		})
	}
}

type sourceFunc func() (*oauth2.Token, error)

func (f sourceFunc) Token() (*oauth2.Token, error) { return f() }

func TestPersistentRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token.json")
	initial := &oauth2.Token{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)}
	if err := Save(path, initial); err != nil {
		t.Fatal(err)
	}
	source := Persistent(sourceFunc(func() (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "new", Expiry: time.Now().Add(time.Hour)}, nil
	}), path, initial)
	tok, err := source.Token()
	if err != nil || tok.RefreshToken != "refresh" {
		t.Fatalf("%v %v", tok, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"access_token":"new"`) || !strings.Contains(string(raw), `"refresh_token":"refresh"`) {
		t.Fatal("token not persisted")
	}
	if err := Save(path, &oauth2.Token{AccessToken: "bad"}); err == nil {
		t.Fatal("missing refresh accepted")
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "bad") {
		t.Fatal("destroyed working token")
	}
}

func TestRedirectValidation(t *testing.T) {
	for _, redirect := range []string{"", "http://example.com/oauth/callback", "https://example.com/wrong", "https://example.com/oauth/callback?x=1"} {
		if err := Run(context.Background(), "missing", "", "", redirect, os.Stdout); err == nil || !strings.Contains(err.Error(), "OAuth") && !strings.Contains(err.Error(), "REDIRECT") {
			t.Fatalf("%s: %v", redirect, err)
		}
	}
}

// A hosting health check must keep succeeding while the one-off job runs and
// after it completes. Authentication expiry must not cancel the job itself.
func TestServeTaskLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			authCtx, expire := context.WithCancel(ctx)
			defer expire()
			done := make(chan error, 1)
			served := make(chan error)
			started := make(chan struct{})
			release := make(chan struct{})
			exited := make(chan error, 1)
			idle := make(chan struct{})
			writer := notifyWriter{idle: idle}
			go func() {
				exited <- serveTask(ctx, authCtx, done, served, writer, func(taskCtx context.Context) error {
					close(started)
					<-release
					if err := taskCtx.Err(); err != nil {
						return err
					}
					if fail {
						return fmt.Errorf("job failed")
					}
					return nil
				})
			}()
			select {
			case <-started:
				t.Fatal("job ran without authorization")
			default:
			}
			done <- nil
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("job did not start")
			}
			expire()
			close(release)
			select {
			case <-idle:
			case <-time.After(time.Second):
				t.Fatal("service did not remain idle after job")
			}
			select {
			case err := <-exited:
				t.Fatalf("service exited after job: %v", err)
			default:
			}
			cancel()
			select {
			case err := <-exited:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("shutdown stuck")
			}
		})
	}
}

type notifyWriter struct{ idle chan struct{} }

func (w notifyWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "HTTP-сервис остаётся") {
		close(w.idle)
	}
	return len(p), nil
}

func TestServeTaskRejectsFailedLogin(t *testing.T) {
	done := make(chan error, 1)
	done <- fmt.Errorf("denied")
	err := serveTask(t.Context(), t.Context(), done, make(chan error), io.Discard, func(context.Context) error { t.Fatal("job ran on failed login"); return nil })
	if err == nil {
		t.Fatal("authorization failure ignored")
	}
}

func TestHealthEndpoint(t *testing.T) {
	h := newHandler(t.Context(), &oauth2.Config{}, "setup", true, nil, make(chan error, 1))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
