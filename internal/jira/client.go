package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config — параметры подключения к Jira.
type Config struct {
	URL string // база вида https://jira.metadoc.kz, без хвостового /
	JQL string
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
		httpClient = http.DefaultClient
	}
	return &Client{cfg: cfg, http: httpClient, pageSize: searchPageSize}
}

const searchPageSize = 100

// jiraTimeLayout — формат поля created в ответе REST API v2.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

// Fetch загружает все страницы поиска и возвращает распознанные задачи релизов.
// Задачи, не прошедшие ParseIssue, пропускаются без записи в лог.
func (c *Client) Fetch(ctx context.Context) ([]ReleaseTask, error) {
	var tasks []ReleaseTask

	startAt := 0
	for {
		page, err := c.search(ctx, startAt)
		if err != nil {
			return nil, err
		}

		for _, iss := range page.Issues {
			created, err := time.Parse(jiraTimeLayout, iss.Fields.Created)
			if err != nil {
				return nil, fmt.Errorf("задача %s: поле created %q: %w", iss.Key, iss.Fields.Created, err)
			}
			if task, ok := ParseIssue(iss.Key, iss.Fields.Summary, iss.Fields.Description, created); ok {
				tasks = append(tasks, task)
			}
		}

		startAt += len(page.Issues)
		if len(page.Issues) == 0 || startAt >= page.Total {
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
		} `json:"fields"`
	} `json:"issues"`
}

func (c *Client) search(ctx context.Context, startAt int) (searchResponse, error) {
	body, err := json.Marshal(map[string]any{
		"jql":        c.cfg.JQL,
		"fields":     []string{"summary", "description", "created"},
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
		return searchResponse{}, fmt.Errorf("запрос к Jira: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return searchResponse{}, fmt.Errorf("Jira ответила %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var out searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return searchResponse{}, fmt.Errorf("разбор ответа Jira: %w", err)
	}
	return out, nil
}
