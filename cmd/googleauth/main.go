// Command googleauth выполняет OAuth-вход пользователя в браузере
// и сохраняет токен для последующего доступа к Google Sheets.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
// получает код через редирект на localhost.
func login(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть локальный порт: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	cfg.RedirectURL = fmt.Sprintf("http://localhost:%d", port)

	state := randomState()
	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "неверный state", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("неверный state в ответе — запрос не от этого запуска")}
			return
		}
		if msg := q.Get("error"); msg != "" {
			http.Error(w, "доступ не предоставлен", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("Google вернул ошибку: %s", msg)}
			return
		}
		fmt.Fprint(w, "Готово, можно закрыть вкладку и вернуться в терминал.")
		resultCh <- result{code: q.Get("code")}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	defer srv.Shutdown(ctx)

	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	fmt.Println("Открой в браузере и войди под своим аккаунтом (должен открыться сам):")
	fmt.Println(authURL)
	openBrowser(authURL)

	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		return cfg.Exchange(ctx, res.code)
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("не дождались входа за 5 минут")
	}
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

func randomState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
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
