// Package gitlab предоставляет сборщик образов.
// Client использует API GitLab, Stub читает встроенные тестовые данные.
package gitlab

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"time"

	"env-release-tracker/internal/release"
	"env-release-tracker/internal/tag"
)

// ImageCollector снимает теги образов со среды.
type ImageCollector interface {
	Collect(ctx context.Context, environment string) (release.EnvState, error)
}

// artifact описывает JSON-результат collect-images.
type artifact struct {
	Environment string    `json:"environment"`
	Namespace   string    `json:"namespace"`
	CollectedAt time.Time `json:"collected_at"`
	Images      []string  `json:"images"`
}

// parseArtifact разбирает images.json в снимок среды.
// Пустой список образов считается ошибкой: он может означать
// как пустой namespace, так и отсутствие доступа к нему.
func parseArtifact(data []byte) (release.EnvState, error) {
	var a artifact
	if err := json.Unmarshal(data, &a); err != nil {
		return release.EnvState{}, fmt.Errorf("разбор images.json: %w", err)
	}
	if len(a.Images) == 0 {
		return release.EnvState{}, fmt.Errorf("%s: пустой список образов — не отличить от ошибки доступа к namespace", a.Environment)
	}

	state := release.EnvState{
		Environment: a.Environment,
		Namespace:   a.Namespace,
		CollectedAt: a.CollectedAt,
	}
	for _, image := range a.Images {
		t, err := tag.ParseImage(image)
		if err != nil {
			state.Unparsed = append(state.Unparsed, image)
			continue
		}
		state.Tags = append(state.Tags, t)
	}
	return state, nil
}

//go:embed testdata/*.json
var fixtures embed.FS

// Stub читает встроенный файл testdata/<environment>.json в формате images.json.
type Stub struct{}

func (Stub) Collect(_ context.Context, environment string) (release.EnvState, error) {
	data, err := fixtures.ReadFile("testdata/" + environment + ".json")
	if err != nil {
		return release.EnvState{}, fmt.Errorf("gitlab: нет тестовых данных для среды %q: %w", environment, err)
	}
	return parseArtifact(data)
}
