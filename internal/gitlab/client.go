package gitlab

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"env-release-tracker/internal/release"
)

// Config задаёт параметры API и запуска collect-images.
type Config struct {
	URL          string
	Token        string
	ProjectID    int
	Ref          string
	PollInterval time.Duration
	// Scenario передаётся как MOCK_SCENARIO только для тестового пайплайна.
	Scenario string
}

// Client получает образы через пайплайн GitLab.
type Client struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger
}

func New(cfg Config, client *http.Client, log *slog.Logger) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("некорректный GITLAB_URL")
	}
	if cfg.Token == "" || cfg.ProjectID < 1 || cfg.Ref == "" || cfg.PollInterval <= 0 {
		return nil, errors.New("GitLab: нужны token, project ID, ref и положительный интервал опроса")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// PRIVATE-TOKEN не должен передаваться при перенаправлении на хранилище артефактов.
	copyClient := *client
	previousRedirect := copyClient.CheckRedirect
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != u.Host || req.URL.Scheme != u.Scheme {
			req.Header.Del("PRIVATE-TOKEN")
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("слишком много перенаправлений")
		}
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &Client{cfg: cfg, http: &copyClient, log: log}, nil
}

type pipeline struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
}
type job struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Collect запускает отдельный пайплайн и читает результат его джобы.
// Предельное время всего сбора задаётся контекстом вызывающего кода.
func (c *Client) Collect(ctx context.Context, environment string) (release.EnvState, error) {
	return c.CollectWithRunner(ctx, environment, "")
}

// CollectWithRunner передаёт точечное переопределение раннера в pipeline.
func (c *Client) CollectWithRunner(ctx context.Context, environment, tagsName string) (release.EnvState, error) {
	variables := []map[string]string{{"key": "ENVIRONMENT", "value": environment}}
	if tagsName != "" {
		variables = append(variables, map[string]string{"key": "tags_name", "value": tagsName})
	}
	if c.cfg.Scenario != "" {
		variables = append(variables, map[string]string{"key": "MOCK_SCENARIO", "value": c.cfg.Scenario})
	}
	body, err := json.Marshal(map[string]any{"ref": c.cfg.Ref, "variables": variables})
	if err != nil {
		return release.EnvState{}, err
	}
	raw, _, err := c.request(ctx, http.MethodPost, "/pipeline", body)
	if err != nil {
		return release.EnvState{}, err
	}
	var p pipeline
	if err := json.Unmarshal(raw, &p); err != nil {
		return release.EnvState{}, fmt.Errorf("ответ запуска pipeline: %w", err)
	}
	if p.ID <= 0 {
		return release.EnvState{}, errors.New("GitLab не вернул ID pipeline")
	}
	c.log.Info("пайплайн GitLab запущен", "env", environment, "pipeline_id", p.ID)
	if err := c.wait(ctx, p.ID); err != nil {
		return release.EnvState{}, err
	}
	j, err := c.findJob(ctx, p.ID)
	if err != nil {
		return release.EnvState{}, err
	}
	var state release.EnvState
	raw, _, err = c.request(ctx, http.MethodGet, fmt.Sprintf("/jobs/%d/artifacts", j.ID), nil)
	if err == nil {
		var body []byte
		if body, err = artifactFromArchive(raw); err == nil {
			state, err = parseArtifact(body)
		}
	}
	if err != nil {
		// В trace уходим только когда результата нет как такового: джоба без
		// артефактов или архив без файла результата. Испорченное содержимое —
		// это ошибка данных, и прятать её за успешным разбором лога нельзя.
		var status *statusError
		missing := errors.Is(err, errNoArtifactFile) || (errors.As(err, &status) && status.code == http.StatusNotFound)
		if !missing {
			return release.EnvState{}, err
		}
		c.log.Info("артефакт отсутствует, чтение лога", "env", environment, "job_id", j.ID)
		raw, _, err = c.request(ctx, http.MethodGet, fmt.Sprintf("/jobs/%d/trace", j.ID), nil)
		if err == nil {
			state, err = parseTrace(raw, environment)
		}
	}
	if err != nil {
		return release.EnvState{}, err
	}
	if state.Environment != environment {
		return release.EnvState{}, errors.New("images.json содержит другое имя среды")
	}
	return state, nil
}

func (c *Client) wait(ctx context.Context, id int) error {
	for {
		raw, _, err := c.request(ctx, http.MethodGet, fmt.Sprintf("/pipelines/%d", id), nil)
		if err != nil {
			return err
		}
		var p pipeline
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("статус pipeline: %w", err)
		}
		switch p.Status {
		case "success":
			return nil
		case "created", "waiting_for_resource", "preparing", "pending", "running", "scheduled":
		default:
			return fmt.Errorf("pipeline %d завершён или требует действия: %s", id, p.Status)
		}
		timer := time.NewTimer(c.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("ожидание pipeline %d: %w", id, ctx.Err())
		case <-timer.C:
		}
	}
}

// artifactSuffix — окончание имени файла с результатом внутри архива джобы.
// Полное имя вида <среда>-<день-месяц-год-час>-collect-images.output содержит
// час по UTC+5: джоба на границе часа или сдвиг часов на раннере сделали бы
// собранное нами имя неверным, поэтому имя только сопоставляется.
const artifactSuffix = "-collect-images.output"

// errNoArtifactFile означает, что архив получен, но результата в нём нет.
var errNoArtifactFile = errors.New("в архиве артефактов нет файла результата")

// artifactFromArchive достаёт результат collect-images из архива артефактов.
func artifactFromArchive(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("архив артефактов: %w", err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, artifactSuffix) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("файл %s: %w", f.Name, err)
		}
		// Предел на распакованный размер: сжатие скрывает настоящий объём,
		// и доверять заявленному в заголовке архива нельзя.
		const maxOutput = 4 << 20
		body, err := io.ReadAll(io.LimitReader(rc, maxOutput+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("чтение %s: %w", f.Name, err)
		}
		if len(body) > maxOutput {
			return nil, fmt.Errorf("файл %s превышает 4 MiB", f.Name)
		}
		return body, nil
	}
	return nil, errNoArtifactFile
}

// maxPipelineDepth ограничивает спуск по дочерним пайплайнам.
// Боевой collect-images строит цепочку родитель → сгенерированный → по среде;
// без предела испорченные данные дали бы бесконечный спуск.
const maxPipelineDepth = 4

// findJob ищет джобу collect-images в пайплайне и его дочерних пайплайнах.
// В запущенном пайплайне её нет: там только генератор и триггер, а сама
// джоба идёт глубже, по одному дочернему пайплайну на среду.
func (c *Client) findJob(ctx context.Context, id int) (job, error) {
	found, err := c.findJobsAt(ctx, id, 0)
	if err != nil {
		return job{}, err
	}
	switch len(found) {
	case 0:
		return job{}, errors.New("джоба collect-images не найдена в запущенном pipeline")
	case 1:
		return found[0], nil
	default:
		// Среду мы запрашиваем ровно одну. Несколько джоб означают, что пайплайн
		// собрал не наш запуск, и любая из них может относиться к чужой среде.
		return job{}, fmt.Errorf("pipeline %d: найдено %d джоб collect-images, ожидалась одна среда", id, len(found))
	}
}

func (c *Client) findJobsAt(ctx context.Context, id, depth int) ([]job, error) {
	if depth >= maxPipelineDepth {
		return nil, fmt.Errorf("pipeline %d: превышена глубина вложенности %d", id, maxPipelineDepth)
	}
	jobs, err := c.jobsOf(ctx, id)
	if err != nil {
		return nil, err
	}
	var found []job
	for _, j := range jobs {
		if j.Name != "collect-images" {
			continue
		}
		if j.Status != "success" {
			return nil, fmt.Errorf("collect-images: статус %s", j.Status)
		}
		if j.ID <= 0 {
			return nil, errors.New("collect-images: отсутствует ID джобы")
		}
		found = append(found, j)
	}
	if len(found) > 0 {
		return found, nil
	}
	children, err := c.bridgesOf(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		// Триггер завершается успехом, не дожидаясь дочернего пайплайна, если
		// на нём не задан strategy: depend. Задаётся он на каждом уровне свой,
		// поэтому дожидаемся сами, а не полагаемся на статус родителя.
		if err := c.wait(ctx, child); err != nil {
			return nil, err
		}
		deeper, err := c.findJobsAt(ctx, child, depth+1)
		if err != nil {
			return nil, err
		}
		found = append(found, deeper...)
	}
	return found, nil
}

// fetchPaged собирает все страницы списочного ответа GitLab.
// Это функция, а не метод: методы в Go не могут иметь параметров типа.
func fetchPaged[T any](ctx context.Context, c *Client, what, path, query string) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		raw, header, err := c.request(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d%s", path, page, query), nil)
		if err != nil {
			return nil, err
		}
		var batch []T
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		all = append(all, batch...)
		if header.Get("X-Next-Page") == "" && len(batch) < 100 {
			return all, nil
		}
	}
}

// jobsOf возвращает все джобы пайплайна. Дочерние пайплайны сюда не попадают:
// GitLab отдаёт их отдельно, через bridges.
func (c *Client) jobsOf(ctx context.Context, id int) ([]job, error) {
	return fetchPaged[job](ctx, c, "список jobs", fmt.Sprintf("/pipelines/%d/jobs", id), "&include_retried=false")
}

// bridge — триггер дочернего пайплайна в ответе GitLab.
type bridge struct {
	Downstream *pipeline `json:"downstream_pipeline"`
}

// bridgesOf возвращает идентификаторы дочерних пайплайнов.
func (c *Client) bridgesOf(ctx context.Context, id int) ([]int, error) {
	bridges, err := fetchPaged[bridge](ctx, c, "список bridges", fmt.Sprintf("/pipelines/%d/bridges", id), "")
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, b := range bridges {
		if b.Downstream != nil && b.Downstream.ID > 0 {
			ids = append(ids, b.Downstream.ID)
		}
	}
	return ids, nil
}

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("GitLab HTTP %d", e.code) }

func (c *Client) request(ctx context.Context, method, path string, body []byte) ([]byte, http.Header, error) {
	endpoint := strings.TrimRight(c.cfg.URL, "/") + "/api/v4/projects/" + strconv.Itoa(c.cfg.ProjectID) + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		// Не включаем URL перенаправления и тело ответа: они могут содержать секреты.
		return nil, nil, errors.New("ошибка HTTP-запроса к GitLab")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, &statusError{code: resp.StatusCode}
	}
	const maxResponse = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, nil, fmt.Errorf("чтение ответа GitLab: %w", err)
	}
	if len(data) > maxResponse {
		return nil, nil, errors.New("ответ GitLab превышает 4 MiB")
	}
	return data, resp.Header, nil
}

var (
	traceANSI   = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	tracePrefix = regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2}T\S+\s+\d{2}[OE][ +]`)
)

func parseTrace(data []byte, environment string) (release.EnvState, error) {
	text := traceANSI.ReplaceAllString(string(data), "")
	text = tracePrefix.ReplaceAllString(text, "")
	const marker = "Namespace: "
	start := strings.Index(text, marker)
	if start < 0 {
		return release.EnvState{}, errors.New("в trace отсутствует Namespace")
	}
	namespace, rest, ok := strings.Cut(text[start+len(marker):], "\n")
	if !ok {
		return release.EnvState{}, errors.New("в trace отсутствует список образов")
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "(no images found)") {
		return release.EnvState{}, errors.New("пустой namespace в trace")
	}
	var images []string
	if err := json.NewDecoder(strings.NewReader(rest)).Decode(&images); err != nil {
		return release.EnvState{}, fmt.Errorf("список образов в trace: %w", err)
	}
	raw, err := json.Marshal(artifact{Environment: environment, Namespace: strings.TrimSpace(namespace), CollectedAt: time.Now().UTC(), Images: images})
	if err != nil {
		return release.EnvState{}, err
	}
	return parseArtifact(raw)
}

var _ ImageCollector = (*Client)(nil)
