package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileRemembersBetweenProcesses(t *testing.T) {
	// Прогон раз в час — это новый процесс каждый раз. Без диска overrides.interval
	// не работает вовсе: память предыдущего прогона недоступна.
	path := filepath.Join(t.TempDir(), "state.json")
	at := time.Now().UTC().Truncate(time.Second)

	first, err := NewFile(path, nil)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	first.Set("nit-adilet", at)

	second, err := NewFile(path, nil)
	if err != nil {
		t.Fatalf("повторный NewFile: %v", err)
	}
	got, ok := second.Get("nit-adilet")
	if !ok {
		t.Fatal("время опроса не сохранилось")
	}
	if !got.Equal(at) {
		t.Errorf("время = %v, хочу %v", got, at)
	}
}

func TestFileMissingStartsEmpty(t *testing.T) {
	// Первый запуск: файла ещё нет, и это не ошибка.
	s, err := NewFile(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	if _, ok := s.Get("nit-adilet"); ok {
		t.Error("пустое хранилище вернуло время")
	}
}

func TestFileRejectsBrokenContent(t *testing.T) {
	// Молча начать с нуля значит опросить все среды заново и не сказать об этом.
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewFile(path, nil); err == nil {
		t.Error("испорченный файл принят молча")
	}
}

func TestFileKeepsOtherEnvironments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewFile(path, nil)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	s.Set("nit-adilet", time.Now())
	s.Set("nit-esedo", time.Now())

	reopened, err := NewFile(path, nil)
	if err != nil {
		t.Fatalf("повторный NewFile: %v", err)
	}
	if _, ok := reopened.Get("nit-adilet"); !ok {
		t.Error("первая среда потеряна")
	}
	if _, ok := reopened.Get("nit-esedo"); !ok {
		t.Error("вторая среда потеряна")
	}
}
