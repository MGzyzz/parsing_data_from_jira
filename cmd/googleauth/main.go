// Command googleauth выполняет OAuth-вход пользователя в браузере
// и сохраняет токен для последующего доступа к Google Sheets.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

func main() {
	secretPath := flag.String("secret", envOr("GOOGLE_CREDENTIALS_JSON", "secrets/client_secret.json"), "путь к OAuth client secret (installed app)")
	tokenPath := flag.String("token", "secrets/token.json", "куда сохранить полученный токен")
	sheetID := flag.String("sheet", os.Getenv("SHEET_ID"), "id таблицы для проверки доступа (пусто — пропустить проверку)")
	flag.Parse()

	ctx := context.Background()

	raw, err := os.ReadFile(*secretPath)
	if err != nil {
		log.Fatalf("чтение client secret: %v", err)
	}
	cfg, err := google.ConfigFromJSON(raw, sheets.SpreadsheetsScope)
	if err != nil {
		log.Fatalf("разбор client secret: %v", err)
	}

	tok, err := login(ctx, cfg)
	if err != nil {
		log.Fatalf("вход: %v", err)
	}

	if err := saveToken(*tokenPath, tok); err != nil {
		log.Fatalf("сохранение токена: %v", err)
	}
	fmt.Printf("Токен сохранён: %s\n", *tokenPath)

	if *sheetID == "" {
		return
	}
	if err := checkAccess(ctx, cfg, tok, *sheetID); err != nil {
		log.Fatalf("проверка доступа к %s: %v\n"+
			"Если это 404 — по допущению D в спеке SHEET_ID сейчас может указывать на папку Drive, а не на саму таблицу; тогда токен ни при чём.", *sheetID, err)
	}
}

// login проходит OAuth loopback-flow (RFC 8252): поднимает локальный
// сервер на свободном порту, открывает consent-экран в браузере и
// получает код через редирект на 127.0.0.1.
func login(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	// IP, а не localhost: Google предупреждает о проблемах localhost с файрволами,
	// а localhost может разрешиться в ::1, когда сервер слушает только IPv4.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть локальный порт: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	state := rand.Text()
	// PKCE связывает код с этим запуском: перехваченный код без verifier
	// на токен не обменять.
	verifier := oauth2.GenerateVerifier()
	results := make(chan callbackResult, 1)

	srv := &http.Server{Handler: callbackHandler(state, results), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(listener)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier),
	)
	fmt.Println("Открой в браузере и войди под своим аккаунтом (должен открыться сам):")
	fmt.Println(authURL)
	openBrowser(authURL)

	select {
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		return cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("не дождались входа за 5 минут")
	}
}

// callbackResult — итог редиректа от Google: код авторизации или ошибка.
type callbackResult struct {
	code string
	err  error
}

// callbackHandler принимает редирект Google на корень сервера.
// Засчитывается только первый ответ: следом браузер запрашивает /favicon.ico,
// могут прийти и повторы. Блокирующая отправка в канал, который уже никто
// не читает, повесила бы обработчик, а с ним и остановку сервера.
func callbackHandler(state string, results chan<- callbackResult) http.Handler {
	deliver := func(res callbackResult) {
		select {
		case results <- res:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "неверный state", http.StatusBadRequest)
			deliver(callbackResult{err: fmt.Errorf("неверный state в ответе — запрос не от этого запуска")})
			return
		}
		if msg := q.Get("error"); msg != "" {
			http.Error(w, "доступ не предоставлен", http.StatusBadRequest)
			deliver(callbackResult{err: fmt.Errorf("Google вернул ошибку: %s", msg)})
			return
		}
		fmt.Fprint(w, "Готово, можно закрыть вкладку и вернуться в терминал.")
		deliver(callbackResult{code: q.Get("code")})
	})
	return mux
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func saveToken(path string, tok *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(tok)
}

// checkAccess проверяет доступ чтением метаданных таблицы.
func checkAccess(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token, sheetID string) error {
	client := cfg.Client(ctx, tok)
	srv, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return fmt.Errorf("клиент sheets: %w", err)
	}
	ss, err := srv.Spreadsheets.Get(sheetID).Fields("properties.title").Context(ctx).Do()
	if err != nil {
		return err
	}
	fmt.Printf("Доступ подтверждён: %q\n", ss.Properties.Title)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
