package gitlab

import (
	"strings"
	"testing"
)

func TestParseArtifactSplitsTagsAndUnparsed(t *testing.T) {
	data := []byte(`{
		"environment": "prod-qazsu",
		"namespace": "prod-qazsu",
		"collected_at": "2026-09-15T10:00:00Z",
		"images": ["redo-backend:main-1.29.13", "postgres", "redo-front:main-1.29.20"]
	}`)

	state, err := parseArtifact(data)
	if err != nil {
		t.Fatalf("parseArtifact: %v", err)
	}

	if state.Environment != "prod-qazsu" || state.Namespace != "prod-qazsu" {
		t.Errorf("environment/namespace = %q/%q", state.Environment, state.Namespace)
	}
	if len(state.Tags) != 2 {
		t.Fatalf("Tags = %d, хочу 2 (postgres без тега не должен туда попасть)", len(state.Tags))
	}
	if len(state.Unparsed) != 1 || state.Unparsed[0] != "postgres" {
		t.Errorf("Unparsed = %v, хочу [\"postgres\"]", state.Unparsed)
	}
	if state.CollectedAt.IsZero() {
		t.Error("CollectedAt не разобран")
	}
}

func TestParseArtifactEmptyImagesIsError(t *testing.T) {
	// §6.3 спеки: скрипт не отличает пустой namespace от ошибки доступа.
	// Пустой список — всегда ошибка, а не "релиза нет", иначе верное
	// значение в реестре затирается пустотой.
	data := []byte(`{"environment": "broken-env", "namespace": "broken-env", "images": []}`)

	_, err := parseArtifact(data)
	if err == nil {
		t.Fatal("пустой images не вернул ошибку")
	}
}

func TestParseArtifactMalformedJSON(t *testing.T) {
	_, err := parseArtifact([]byte(`not json`))
	if err == nil {
		t.Fatal("некорректный JSON не вернул ошибку")
	}
}

func TestStubCollectReadsFixture(t *testing.T) {
	state, err := Stub{}.Collect(t.Context(), "prod-qazsu")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if state.Environment != "prod-qazsu" {
		t.Errorf("Environment = %q, хочу prod-qazsu", state.Environment)
	}
	if len(state.Tags) != 6 {
		t.Errorf("Tags = %d, хочу 6", len(state.Tags))
	}
}

func TestStubCollectPropagatesEmptyImagesError(t *testing.T) {
	_, err := Stub{}.Collect(t.Context(), "broken-env")
	if err == nil {
		t.Fatal("broken-env: ошибка не вернулась")
	}
}

func TestStubCollectUnknownEnvironment(t *testing.T) {
	_, err := Stub{}.Collect(t.Context(), "no-such-env")
	if err == nil {
		t.Fatal("несуществующая среда не вернула ошибку")
	}
	if !strings.Contains(err.Error(), "no-such-env") {
		t.Errorf("ошибка %q не называет среду", err)
	}
}

// var _ ImageCollector = Stub{} — компилируемая проверка, что Stub
// реализует интерфейс, которым будет пользоваться internal/app.
var _ ImageCollector = Stub{}
