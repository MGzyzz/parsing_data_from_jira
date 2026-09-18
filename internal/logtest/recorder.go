// Package logtest собирает записи slog для проверок в тестах.
// Импортируется только из _test.go, в бинарник не попадает.
package logtest

import (
	"context"
	"log/slog"
	"sync"
)

// Recorder — slog.Handler, запоминающий все записи.
type Recorder struct {
	mu      sync.Mutex
	records []slog.Record
}

// New возвращает логгер и рекордер, в который тот пишет.
func New() (*slog.Logger, *Recorder) {
	r := &Recorder{}
	return slog.New(r), r
}

func (*Recorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *Recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}

// Обработчик один на всё дерево: атрибуты и группы в тестах не используются.
func (r *Recorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *Recorder) WithGroup(string) slog.Handler      { return r }

// Entry — то, что проверяют тесты: уровень и атрибуты записи.
// Значения атрибутов приводятся к строке, сравнения с образцом этого хватает.
type Entry struct {
	Level slog.Level
	Attrs map[string]string
}

// Find возвращает первую запись с указанным сообщением.
func (r *Recorder) Find(message string) (Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.Message != message {
			continue
		}
		attrs := map[string]string{}
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		return Entry{Level: rec.Level, Attrs: attrs}, true
	}
	return Entry{}, false
}

// Count считает записи с указанным сообщением.
func (r *Recorder) Count(message string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int
	for _, rec := range r.records {
		if rec.Message == message {
			n++
		}
	}
	return n
}
