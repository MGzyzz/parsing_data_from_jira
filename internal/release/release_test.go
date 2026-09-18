package release

import (
	"testing"
	"time"

	"env-release-tracker/internal/tag"
)

// cfg — настройки из ТЗ: смещение 39 и пять кор-сервисов.
func cfg() Config {
	return Config{
		MinorOffset: 39,
		CoreServices: []string{
			"redo-nuxeo", "redo-backend", "redo-camunda",
			"redo-front", "redo-integration",
		},
	}
}

// images собирает состояние среды из строк вида "redo-backend:main-1.29.0".
func images(t *testing.T, env string, refs ...string) EnvState {
	t.Helper()
	st := EnvState{Environment: env, CollectedAt: time.Now()}
	for _, r := range refs {
		parsed, err := tag.ParseImage(r)
		if err != nil {
			st.Unparsed = append(st.Unparsed, r)
			continue
		}
		st.Tags = append(st.Tags, parsed)
	}
	return st
}

// baseline собирает базовые теги задачи Release N из строк секции Projects.
func baseline(t *testing.T, number int, lines ...string) *Baseline {
	t.Helper()
	b := &Baseline{Number: number, Tags: map[string]tag.Tag{}}
	for _, l := range lines {
		parsed, ok, err := tag.ParseProjectLine(l)
		if err != nil || !ok {
			t.Fatalf("baseline: строка %q не разобралась: ok=%v err=%v", l, ok, err)
		}
		b.Tags[parsed.Service] = parsed
	}
	return b
}

// release68 — базовые теги Release 68 из DOPS-4820.
func release68(t *testing.T) *Baseline {
	t.Helper()
	return baseline(t, 68,
		"# nuxeo: main-1.29.0",
		"# backend: main-1.29.0",
		"# camunda: main-1.29.0",
		"# front: main-1.29.0",
		"# integration: main-1.29.0",
		"# notification: main-1.29.0",
		"# redo-email: main-1.29.0",
	)
}

func TestComputeCleanRelease(t *testing.T) {
	st := images(t, "prod-qazsu",
		"redo-nuxeo:main-1.29.0",
		"redo-backend:main-1.29.0",
		"redo-camunda:main-1.29.0",
		"redo-front:main-1.29.0",
		"redo-integration:main-1.29.0",
	)

	got := Compute(st, release68(t), cfg())

	if got.Release != 68 {
		t.Errorf("Release = %d, хочу 68 (minor 29 + смещение 39)", got.Release)
	}
	if got.HasHF {
		t.Error("HasHF = true, хочу false: все сервисы на базовых тегах релиза")
	}
	if got.MinorMismatch {
		t.Error("MinorMismatch = true, хочу false: кор-сервисы на одном minor")
	}
	if got.Cell() != "Release 68" {
		t.Errorf("Cell() = %q, хочу %q", got.Cell(), "Release 68")
	}
}

func TestComputeHotfixOnCoreService(t *testing.T) {
	// Реальное состояние после HF к Release 68: патчи backend, camunda и front
	// ушли вперёд базового тега.
	st := images(t, "prod-qazsu",
		"redo-nuxeo:main-1.29.0",
		"redo-backend:main-1.29.13",
		"redo-camunda:main-1.29.12",
		"redo-front:main-1.29.20",
		"redo-integration:main-1.29.0",
	)

	got := Compute(st, release68(t), cfg())

	if got.Release != 68 {
		t.Errorf("Release = %d, хочу 68: HF двигает патч, а не minor", got.Release)
	}
	if !got.HasHF {
		t.Error("HasHF = false, хочу true: патч кор-сервиса выше базового тега")
	}
	if got.Cell() != "Release 68 + HF" {
		t.Errorf("Cell() = %q, хочу %q", got.Cell(), "Release 68 + HF")
	}
}

func TestComputeHotfixOnNonCoreService(t *testing.T) {
	// У не-кор сервисов своя нумерация: redo-email идёт по 1.29.x только
	// потому, что так записано в задаче. Правило N-39 к нему не применяется,
	// признак HF даёт лишь сравнение с Jira.
	st := images(t, "prod-qazsu",
		"redo-nuxeo:main-1.29.0",
		"redo-backend:main-1.29.0",
		"redo-camunda:main-1.29.0",
		"redo-front:main-1.29.0",
		"redo-integration:main-1.29.0",
		"redo-email:main-1.29.4",
	)

	got := Compute(st, release68(t), cfg())

	if got.Release != 68 {
		t.Errorf("Release = %d, хочу 68", got.Release)
	}
	if !got.HasHF {
		t.Error("HasHF = false, хочу true: не-кор сервис новее своего тега в задаче")
	}
}

func TestComputeUnknownServiceDoesNotTriggerHotfix(t *testing.T) {
	// Сервиса нет в задаче Release N - на HF он не влияет, но должен быть виден.
	st := images(t, "prod-qazsu",
		"redo-nuxeo:main-1.29.0",
		"redo-backend:main-1.29.0",
		"redo-camunda:main-1.29.0",
		"redo-front:main-1.29.0",
		"redo-integration:main-1.29.0",
		"redo-calendar:main-1.7.2",
	)

	got := Compute(st, release68(t), cfg())

	if got.HasHF {
		t.Error("HasHF = true, хочу false: сервиса нет в задаче релиза, судить не по чему")
	}
	if len(got.Details) == 0 {
		t.Error("Details пуст: сервис вне задачи релиза должен попасть в детали")
	}
}

func TestComputeTakesHigherTagDuringRollout(t *testing.T) {
	// Во время накатки в подах живут два тега одного сервиса.
	st := images(t, "prod-qazsu",
		"redo-nuxeo:main-1.29.0",
		"redo-backend:main-1.29.0",
		"redo-backend:main-1.29.13",
		"redo-camunda:main-1.29.0",
		"redo-front:main-1.29.0",
		"redo-integration:main-1.29.0",
	)

	got := Compute(st, release68(t), cfg())

	if !got.HasHF {
		t.Error("HasHF = false, хочу true: из двух тегов берётся больший, 1.29.13")
	}
}

func TestComputeMinorMismatchTakesLower(t *testing.T) {
	// Кор-сервисы разъехались по minor: часть на Release 67, часть на 68.
	st := images(t, "prod-holding",
		"redo-nuxeo:main-1.28.0",
		"redo-backend:main-1.29.0",
		"redo-camunda:main-1.28.0",
		"redo-front:main-1.28.0",
		"redo-integration:main-1.28.0",
	)

	got := Compute(st, nil, cfg())

	if got.Release != 67 {
		t.Errorf("Release = %d, хочу 67: при расхождении берём меньший minor", got.Release)
	}
	if !got.MinorMismatch {
		t.Error("MinorMismatch = false, хочу true")
	}
	if len(got.Details) == 0 {
		t.Error("Details пуст: расхождение по minor надо перечислить")
	}
}

func TestComputeWithoutCoreServices(t *testing.T) {
	// Офисная среда: образы собраны из веток, кор-сервисов с версионными
	// тегами нет. Релиз не определён - значение в реестре трогать нельзя.
	st := images(t, "dev",
		"redo-backend:feature-DOPS-1234",
		"busybox",
	)

	got := Compute(st, release68(t), cfg())

	if got.Defined() {
		t.Errorf("Defined() = true при Release %d, хочу false", got.Release)
	}
	if got.Cell() != "" {
		t.Errorf("Cell() = %q, хочу пустую строку: писать нечего", got.Cell())
	}
}

func TestComputeWithoutBaselineMarksMismatch(t *testing.T) {
	// Задачи Release N в Jira не нашлось: значение всё равно пишем,
	// но помечаем расхождение - сверка не имеет права вето.
	st := images(t, "prod-qazsu",
		"redo-nuxeo:main-1.29.0",
		"redo-backend:main-1.29.0",
		"redo-camunda:main-1.29.0",
		"redo-front:main-1.29.0",
		"redo-integration:main-1.29.0",
	)

	got := Compute(st, nil, cfg())

	if got.Release != 68 {
		t.Errorf("Release = %d, хочу 68: релиз считается по тегам, а не по Jira", got.Release)
	}
	if !got.JiraMismatch {
		t.Error("JiraMismatch = false, хочу true: задачи Release 68 нет")
	}
	if got.Cell() != "Release 68" {
		t.Errorf("Cell() = %q, хочу %q: сверка не блокирует запись", got.Cell(), "Release 68")
	}
}

func TestComputeCoreServiceAheadWithoutBaseline(t *testing.T) {
	// Задачи релиза нет, но патч кор-сервиса не нулевой - значит HF был.
	st := images(t, "prod-qazsu",
		"redo-nuxeo:main-1.29.0",
		"redo-backend:main-1.29.13",
		"redo-camunda:main-1.29.0",
		"redo-front:main-1.29.0",
		"redo-integration:main-1.29.0",
	)

	got := Compute(st, nil, cfg())

	if !got.HasHF {
		t.Error("HasHF = false, хочу true: без задачи релиза база патча считается нулевой")
	}
}

func TestIneligibleTagsCannotWinRolloutOrTriggerHF(t *testing.T) {
	st := images(t, "prod-a", "redo-backend:main-1.29.0", "redo-backend:feature-2.99.99", "redo-backend:release-2.99.99", "redo-email:feature-1.99.99")
	got := Compute(st, release68(t), cfg())
	if got.Release != 68 || got.HasHF || len(got.Details) < 3 {
		t.Fatalf("result=%+v", got)
	}
	only := Compute(images(t, "prod-a", "redo-backend:feature-2.29.13"), nil, cfg())
	if only.Defined() {
		t.Fatalf("ineligible release=%+v", only)
	}
}

func TestJiraHotfixVerification(t *testing.T) {
	st := images(t, "prod-a", "redo-backend:main-1.29.13")
	base := release68(t)
	missing := Compute(st, base, cfg())
	if !missing.HasHF || !missing.JiraMismatch || missing.Cell() != "Release 68 + HF" {
		t.Fatalf("missing=%+v", missing)
	}
	base.Hotfixes = []map[string]tag.Tag{{"redo-backend": st.Tags[0]}}
	matched := Compute(st, base, cfg())
	if matched.JiraMismatch || !matched.HasHF {
		t.Fatalf("matched=%+v", matched)
	}
	wrong := baseline(t, 68, "backend: main-1.28.99")
	got := Compute(st, wrong, cfg())
	if !got.JiraMismatch || !got.HasHF || got.Release != 68 {
		t.Fatalf("invalid base masked HF: %+v", got)
	}
}

func TestNonCoreOwnVersionAndExactHFTag(t *testing.T) {
	base := baseline(t, 68, "backend: main-1.29.0", "email: main-1.9.6")
	st := images(t, "prod-a", "redo-backend:main-1.29.0", "redo-email:main-1.9.7")
	base.Hotfixes = []map[string]tag.Tag{{"redo-email": st.Tags[1]}}
	got := Compute(st, base, cfg())
	if got.Release != 68 || !got.HasHF || got.JiraMismatch {
		t.Fatalf("result=%+v", got)
	}
	ht := st.Tags[1]
	ht.Branch = "release"
	base.Hotfixes[0]["redo-email"] = ht
	got = Compute(st, base, cfg())
	if !got.HasHF || !got.JiraMismatch {
		t.Fatalf("wrong branch confirmed: %+v", got)
	}
}

func TestThirdPartyBaselineDoesNotCreateMismatch(t *testing.T) {
	base := baseline(t, 68, "backend: main-1.29.0", "onlyoffice-documentserver-unlimited: 8.3.3")
	got := Compute(images(t, "prod-a", "redo-backend:main-1.29.0", "onlyoffice-documentserver-unlimited:8.3.4"), base, cfg())
	if got.HasHF || got.JiraMismatch || got.Release != 68 {
		t.Fatalf("third-party affected result: %+v", got)
	}
}

func TestComputeSyntheticRelease69(t *testing.T) {
	st := images(t, "prod-holding",
		"redo-nuxeo:main-1.30.0",
		"redo-backend:main-1.30.0",
		"redo-camunda:main-1.30.0",
		"redo-front:main-1.30.0",
		"redo-integration:main-1.30.0",
	)
	got := Compute(st, nil, cfg())
	if got.Cell() != "Release 69" || got.HasHF || got.MinorMismatch || !got.JiraMismatch {
		t.Fatalf("result=%+v", got)
	}
	t.Logf("env=%s release=%q minor_mismatch=%v jira_mismatch=%v", got.Environment, got.Cell(), got.MinorMismatch, got.JiraMismatch)
}

func TestComputeFlagsCoreServiceWithoutVersionTag(t *testing.T) {
	// kpo-prod: фронт собран из ветки среды. Релиз считается по остальным
	// кор-сервисам, но выпадение фронта из расчёта должно быть заметно.
	st := images(t, "kpo-prod",
		"redo-nuxeo:main-1.29.4",
		"redo-backend:main-1.29.17",
		"redo-camunda:main-1.29.14",
		"redo-front:kpo-prod-164897",
		"redo-integration:main-1.29.2",
	)

	got := Compute(st, nil, cfg())

	if got.Cell() != "Release 68 + HF" {
		t.Errorf("Cell() = %q, хочу %q", got.Cell(), "Release 68 + HF")
	}
	if !got.CoreUnversioned {
		t.Error("CoreUnversioned = false, хочу true: redo-front без версионного тега")
	}
	if !contains(got.Details, "кор-сервис без версионного тега: redo-front:kpo-prod-164897") {
		t.Errorf("Details = %v: нет пометки о redo-front", got.Details)
	}
}

func TestComputeCoreServiceWithTagOutsideReleaseSchemeIsFlagged(t *testing.T) {
	st := images(t, "prod-a", "redo-backend:main-1.29.0", "redo-front:feature-1.29.0")

	got := Compute(st, nil, cfg())

	if !got.CoreUnversioned {
		t.Errorf("CoreUnversioned = false: тег ветки feature не по схеме релиза, details=%v", got.Details)
	}
}

func TestComputeCoreRolloutFromBranchBuildIsNotFlagged(t *testing.T) {
	// Идёт накатка с кастомной сборки на версионную: версионный тег уже есть,
	// сервис участвует в расчёте, пометка не нужна.
	st := images(t, "kpo-prod", "redo-backend:main-1.29.0", "redo-front:kpo-prod-164897", "redo-front:main-1.29.0")

	got := Compute(st, nil, cfg())

	if got.CoreUnversioned {
		t.Errorf("CoreUnversioned = true, хочу false: details=%v", got.Details)
	}
}

func TestComputeNonCoreWithoutVersionTagIsNotFlagged(t *testing.T) {
	st := images(t, "kpo-prod", "redo-backend:main-1.29.0", "redo-check-docs:kpo-prod-145460")

	if got := Compute(st, nil, cfg()); got.CoreUnversioned {
		t.Errorf("CoreUnversioned = true для не-кор сервиса: details=%v", got.Details)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestComputeDoesNotRepeatUnversionedCoreImageInDetails(t *testing.T) {
	// redo-front одновременно и неразобранный образ, и кор-сервис без версии.
	// Пометка о кор-сервисе информативнее, второй строки про тот же образ быть
	// не должно: в деталях kpo-prod таких дублей набирается заметно.
	st := images(t, "kpo-prod",
		"redo-nuxeo:main-1.29.4",
		"redo-backend:main-1.29.17",
		"redo-front:kpo-prod-164897",
	)

	got := Compute(st, nil, cfg())

	if !contains(got.Details, "кор-сервис без версионного тега: redo-front:kpo-prod-164897") {
		t.Fatalf("Details = %v: нет пометки о кор-сервисе", got.Details)
	}
	if contains(got.Details, "образ вне схемы версий: redo-front:kpo-prod-164897") {
		t.Errorf("Details = %v: тот же образ указан дважды", got.Details)
	}
}
