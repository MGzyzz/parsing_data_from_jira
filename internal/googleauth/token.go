// Package googleauth provides server-side Google OAuth and durable tokens.
package googleauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
)

func TokenPath() string {
	if p := os.Getenv("GOOGLE_TOKEN_FILE"); p != "" {
		return p
	}
	return "secrets/token.json"
}

// Save atomically replaces the token, keeping credentials private.
func Save(path string, token *oauth2.Token) error {
	if token.RefreshToken == "" {
		return fmt.Errorf("Google не выдал refresh token; повторите вход с согласием на offline-доступ")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".oauth-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(token); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type persistentSource struct {
	mu     sync.Mutex
	source oauth2.TokenSource
	path   string
	last   oauth2.Token
}

func Persistent(source oauth2.TokenSource, path string, initial *oauth2.Token) oauth2.TokenSource {
	return &persistentSource{source: source, path: path, last: *initial}
}

func (s *persistentSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.source.Token()
	if err != nil {
		return nil, fmt.Errorf("не удалось обновить Google OAuth: проверьте соединение; если доступ отозван или истёк, повторите вход")
	}
	copy := *tok
	if copy.RefreshToken == "" {
		copy.RefreshToken = s.last.RefreshToken
	}
	if copy.AccessToken != s.last.AccessToken || copy.RefreshToken != s.last.RefreshToken || !copy.Expiry.Equal(s.last.Expiry) {
		if err := Save(s.path, &copy); err != nil {
			return nil, fmt.Errorf("сохранение Google OAuth: %w", err)
		}
		s.last = copy
	}
	return &copy, nil
}
