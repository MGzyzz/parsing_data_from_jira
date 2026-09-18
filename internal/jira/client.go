package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Config — параметры подключения к Jira.
type Config struct {
	URL          string // база вида https://jira.metadoc.kz, без хвостового /
	JQL          string
	FetchTimeout time.Duration
	Logger       *slog.Logger
}

// Client читает задачи релизов через REST API v2.
type Client struct {
	cfg  Config
	http *http.Client
	// pageSize задаёт размер страницы поиска; тесты могут уменьшать его.
	pageSize int
}

// New создаёт клиент Jira с заданным HTTP-клиентом.
// Вызывающий код настраивает авторизацию и транспорт ReadOnly.
func New(cfg Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = 2 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Client{cfg: cfg, http: httpClient, pageSize: searchPageSize}
}

const searchPageSize = 100

// jiraTimeLayout — формат поля created в ответе REST API v2.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

// Fetch загружает все страницы поиска и возвращает распознанные задачи релизов.
// Ошибки отдельных задач диагностируются и не прерывают остальные страницы.
func (c *Client) Fetch(ctx context.Context) ([]ReleaseTask, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()
	var tasks []ReleaseTask
	// Под JQL попадают сотни нерелизных и незавершённых задач. Это обычный
	// состав выборки, а не событие: в лог уходит их количество, не перечень.
	// Неразобранные строки — тоже состояние данных, одно и то же от прогона
	// к прогону, поэтому их перечень уходит в DEBUG, а в сводке остаётся счёт.
	var notDone, notRelease, skippedLines int

	startAt := 0
	for {
		page, err := c.search(ctx, startAt)
		if err != nil {
			return nil, err
		}

		for _, iss := range page.Issues {
			if iss.Fields.Status.Category.Key != "done" {
				notDone++
				continue
			}
			created, err := time.Parse(jiraTimeLayout, iss.Fields.Created)
			if err != nil {
				c.cfg.Logger.Warn("задача Jira пропущена", "key", iss.Key, "reason", "некорректная дата created")
				continue
			}
			task, ok := ParseIssue(iss.Key, iss.Fields.Summary, iss.Fields.Description, created)
			if !ok {
				notRelease++
				continue
			}
			// Текст строки — единственное, по чему можно исправить данные в Jira.
			skippedLines += len(task.Skipped)
			for _, line := range task.Skipped {
				c.cfg.Logger.Debug("строка Jira пропущена", "key", iss.Key, "line", line)
			}
			tasks = append(tasks, task)
		}

		startAt += len(page.Issues)
		if len(page.Issues) == 0 || startAt >= page.Total {
			c.cfg.Logger.Info("разбор Jira завершён", "recognized", len(tasks),
				"skipped_not_release", notRelease, "skipped_not_done", notDone,
				"skipped_lines", skippedLines)
			return tasks, nil
		}
	}
}

type searchResponse struct {
	Total  int `json:"total"`
	Issues []struct {
		Key    string `json:"key"`
		Fields struct {
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Created     string `json:"created"`
			Status      struct {
				Category struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"status"`
		} `json:"fields"`
	} `json:"issues"`
}

func (c *Client) search(ctx context.Context, startAt int) (searchResponse, error) {
	body, err := json.Marshal(map[string]any{
		"jql":        c.cfg.JQL,
		"fields":     []string{"summary", "description", "created", "status"},
		"startAt":    startAt,
		"maxResults": c.pageSize,
	})
	if err != nil {
		return searchResponse{}, fmt.Errorf("тело запроса к Jira: %w", err)
	}

	url := strings.TrimRight(c.cfg.URL, "/") + "/rest/api/2/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return searchResponse{}, fmt.Errorf("запрос к Jira: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return searchResponse{}, ctx.Err()
		}
		return searchResponse{}, fmt.Errorf("ошибка HTTP-запроса к Jira")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return searchResponse{}, fmt.Errorf("Jira ответила HTTP %d", resp.StatusCode)
	}

	var out searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return searchResponse{}, fmt.Errorf("разбор ответа Jira: %w", err)
	}
	return out, nil
}
