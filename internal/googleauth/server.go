package googleauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/sheets/v4"
)

// Run serves a single, explicitly initiated login. It never runs the tracker.
func Run(ctx context.Context, credentials, tokenPath, listen, redirect string, out io.Writer) error {
	return run(ctx, credentials, tokenPath, listen, redirect, out, nil)
}

// RunWithTask keeps HTTP alive and runs task once after successful authorization.
// Token storage is still ephemeral unless tokenPath is on persistent storage.
func RunWithTask(ctx context.Context, credentials, tokenPath, listen, redirect string, out io.Writer, task func(context.Context) error) error {
	return run(ctx, credentials, tokenPath, listen, redirect, out, task)
}

func run(ctx context.Context, credentials, tokenPath, listen, redirect string, out io.Writer, task func(context.Context) error) error {
	u, err := url.Parse(redirect)
	if err != nil || u.Host == "" || u.Path != "/oauth/callback" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("GOOGLE_OAUTH_REDIRECT_URL должен иметь вид https://host/oauth/callback")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return fmt.Errorf("для серверного OAuth требуется HTTPS; HTTP разрешён только на localhost")
	}
	raw, err := os.ReadFile(credentials)
	if err != nil {
		return err
	}
	var probe struct {
		Web json.RawMessage `json:"web"`
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.Web) == 0 {
		return fmt.Errorf("нужен JSON OAuth client типа Web application")
	}
	cfg, err := google.ConfigFromJSON(raw, sheets.SpreadsheetsScope)
	if err != nil {
		return err
	}
	cfg.RedirectURL = redirect
	authCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	setup := rand.Text()
	done := make(chan error, 1)
	handler := newHandler(authCtx, cfg, setup, u.Scheme == "https", func(tok *oauth2.Token) error { return Save(tokenPath, tok) }, done)
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 30 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		srv.Shutdown(shutdown)
	}()
	u.Path = "/oauth/start"
	u.RawQuery = url.Values{"key": {setup}}.Encode()
	fmt.Fprintf(out, "Откройте приватную ссылку в своём браузере (действует 10 минут):\n%s\n", u.String())
	if task != nil {
		return serveTask(ctx, authCtx, done, served, out, task)
	}
	select {
	case err := <-done:
		return err
	case err := <-served:
		return err
	case <-authCtx.Done():
		return authCtx.Err()
	}

}

// serveTask keeps the service alive even when the single run fails, avoiding
// automatic job repeats caused by a hosting platform restarting the process.
func serveTask(ctx, authCtx context.Context, done <-chan error, served <-chan error, out io.Writer, task func(context.Context) error) error {
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case err := <-served:
		return err
	case <-authCtx.Done():
		return authCtx.Err()
	}
	fmt.Fprintln(out, "Google подключён; запускается один проверочный прогон без записи")
	completed := make(chan error, 1)
	go func() { completed <- task(ctx) }()
	select {
	case err := <-completed:
		if err != nil {
			fmt.Fprintln(out, "Проверочный прогон завершился с ошибкой; подробности выше в логах")
		} else {
			fmt.Fprintln(out, "Проверочный прогон завершён; результат смотрите в логах")
		}
	case err := <-served:
		return err
	case <-ctx.Done():
		return nil
	}
	fmt.Fprintln(out, "HTTP-сервис остаётся запущенным. Автоматического повторного прогона нет")
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
		return nil
	}
}

func newHandler(ctx context.Context, cfg *oauth2.Config, setup string, secure bool, save func(*oauth2.Token) error, done chan<- error) http.Handler {
	var mu sync.Mutex
	var state, verifier string
	var used bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK); fmt.Fprint(w, "ok") })
	mux.HandleFunc("GET /oauth/start", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), []byte(setup)) != 1 {
			http.Error(w, "Недействительная ссылка", http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if used || state != "" {
			http.Error(w, "Вход уже начат; при необходимости перезапустите -google-auth", http.StatusConflict)
			return
		}
		state, verifier = rand.Text(), oauth2.GenerateVerifier()
		http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: state, Path: "/oauth/callback", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
		http.Redirect(w, r, cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
	})
	mux.HandleFunc("GET /oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookie, err := r.Cookie("oauth_state")
		if used || state == "" || err != nil || cookie.Value != state || r.URL.Query().Get("state") != state {
			mu.Unlock()
			http.Error(w, "Недействительный сеанс входа", http.StatusBadRequest)
			return
		}
		used = true
		v := verifier
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "oauth_state", Path: "/oauth/callback", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
		if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
			http.Error(w, "Доступ не предоставлен. Повторите запуск -google-auth.", http.StatusBadRequest)
			done <- fmt.Errorf("пользователь не предоставил доступ Google")
			return
		}
		exchangeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		tok, err := cfg.Exchange(exchangeCtx, r.URL.Query().Get("code"), oauth2.VerifierOption(v))
		if err == nil {
			err = save(tok)
		}
		if err != nil {
			http.Error(w, "Не удалось сохранить доступ. Повторите запуск -google-auth.", http.StatusBadGateway)
			// OAuth error bodies may contain credentials; do not send them to logs.
			done <- fmt.Errorf("не удалось обменять код или сохранить токен; проверьте OAuth client, redirect URL и права на каталог токенов")
			return
		}
		fmt.Fprint(w, "Google подключён. Можно закрыть вкладку. Результат работы смотрите в логах сервиса. Для записи в таблицу аккаунту нужны права редактора.")
		done <- nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}
