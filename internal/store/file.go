// Package store хранит время последнего успешного опроса сред между запусками.
//
// Нужен для overrides.interval: сервис запускается раз в час новым процессом,
// и без диска «опрашивать раз в сутки» не работает вовсе.
package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// File — хранилище в JSON-файле. Безопасен для параллельного использования:
// среды опрашиваются пулом воркеров.
type File struct {
	path string
	log  *slog.Logger

	mu   sync.Mutex
	data map[string]time.Time
}

// NewFile читает файл состояния. Отсутствие файла — не ошибка: так выглядит
// первый запуск. Нечитаемое содержимое ошибка: начать с нуля молча значит
// опросить все среды заново и не сказать об этом.
func NewFile(path string, log *slog.Logger) (*File, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &File{path: path, log: log, data: map[string]time.Time{}}

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("чтение файла состояния %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("разбор файла состояния %s: %w", path, err)
	}
	return s, nil
}

func (s *File) Get(environment string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.data[environment]
	return at, ok
}

// Set запоминает время опроса и сразу сохраняет файл: прогон может быть
// прерван на любой среде, а несохранённое время означает лишний опрос.
func (s *File) Set(environment string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[environment] = at
	if err := s.save(); err != nil {
		// Потеря времени опроса стоит одного лишнего пайплайна, а падение
		// прогона — целого часа данных, поэтому только предупреждение.
		s.log.Warn("не удалось сохранить время опроса", "env", environment, "path", s.path, "err", err)
	}
}

// save пишет во временный файл и переименовывает: прерванная запись
// не оставит вместо состояния обрезанный JSON.
func (s *File) save() error {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("временный файл состояния: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("запись состояния: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрытие состояния: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("права на файл состояния: %w", err)
	}
	return os.Rename(tmp.Name(), s.path)
}
