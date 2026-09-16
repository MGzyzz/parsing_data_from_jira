// Package release определяет релиз по версиям образов.
// Базовые теги Jira используются для расчёта HF и признака расхождения.
package release

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"env-release-tracker/internal/tag"
)

// EnvState — снимок среды: что реально стоит в её namespace.
type EnvState struct {
	Environment string
	Namespace   string
	Tags        []tag.Tag // разобранные теги образов
	Unparsed    []string  // образы с чужой схемой тегов: в детали, не в расчёт
	CollectedAt time.Time
}

// Baseline — базовые теги задачи Release N из Jira.
type Baseline struct {
	Number int
	Tags   map[string]tag.Tag // сервис -> тег из секции Projects
}

// Config — правила расчёта, приходят из переменных окружения.
type Config struct {
	// MinorOffset связывает номер релиза с minor: Release N <-> minor = N - offset.
	MinorOffset int
	// CoreServices содержит имена сервисов, по которым определяется номер релиза.
	CoreServices []string
}

// Result — что получилось по среде за прогон.
type Result struct {
	Environment   string
	Release       int // 0 означает «не определён»
	HasHF         bool
	MinorMismatch bool // кор-сервисы разъехались по minor
	JiraMismatch  bool // сверка с задачей релиза не сошлась
	Details       []string
	CollectedAt   time.Time
}

// Defined сообщает, удалось ли определить релиз.
func (r Result) Defined() bool { return r.Release > 0 }

// Cell форматирует результат для колонки Release: Release N или Release N + HF.
func (r Result) Cell() string {
	if !r.Defined() {
		return ""
	}
	if r.HasHF {
		return fmt.Sprintf("Release %d + HF", r.Release)
	}
	return fmt.Sprintf("Release %d", r.Release)
}

// Compute вычисляет релиз среды по её тегам, сверяясь с базовыми тегами Jira.
// base может быть nil: задачи Release N в Jira может не оказаться.
func Compute(state EnvState, base *Baseline, cfg Config) Result {
	res := Result{Environment: state.Environment, CollectedAt: state.CollectedAt}

	for _, image := range state.Unparsed {
		res.Details = append(res.Details, "образ вне схемы версий: "+image)
	}

	latest := latestByService(state.Tags)
	core := coreSet(cfg.CoreServices)

	minor, mismatch, details, ok := coreMinor(latest, core)
	res.Details = append(res.Details, details...)
	if !ok {
		// Кор-сервисов с версионными тегами нет: офисная среда или сбой сбора.
		// Релиз не определён, значение в реестре трогать нельзя.
		sort.Strings(res.Details)
		return res
	}

	res.Release = minor + cfg.MinorOffset
	res.MinorMismatch = mismatch
	res.JiraMismatch = base == nil || base.Number != res.Release

	hf, hfDetails := hasHotfix(latest, core, base)
	res.HasHF = hf
	res.Details = append(res.Details, hfDetails...)

	sort.Strings(res.Details)
	return res
}

// latestByService выбирает максимальную версию каждого сервиса.
// Несколько версий могут присутствовать одновременно во время обновления.
func latestByService(tags []tag.Tag) map[string]tag.Tag {
	latest := make(map[string]tag.Tag, len(tags))
	for _, t := range tags {
		if cur, ok := latest[t.Service]; ok && t.Version.Compare(cur.Version) <= 0 {
			continue
		}
		latest[t.Service] = t
	}
	return latest
}

func coreSet(services []string) map[string]bool {
	set := make(map[string]bool, len(services))
	for _, s := range services {
		set[s] = true
	}
	return set
}

// coreMinor возвращает минимальный minor среди найденных кор-сервисов.
// Различающиеся значения minor отмечаются как расхождение.
func coreMinor(latest map[string]tag.Tag, core map[string]bool) (minor int, mismatch bool, details []string, ok bool) {
	seen := map[int][]string{}
	for service := range core {
		t, found := latest[service]
		if !found {
			continue
		}
		seen[t.Version.Minor] = append(seen[t.Version.Minor], service)
	}
	if len(seen) == 0 {
		return 0, false, []string{"кор-сервисы с версионными тегами не найдены"}, false
	}

	minors := make([]int, 0, len(seen))
	for m := range seen {
		minors = append(minors, m)
	}
	sort.Ints(minors)

	if len(minors) > 1 {
		for _, m := range minors {
			services := seen[m]
			sort.Strings(services)
			details = append(details, fmt.Sprintf("minor %d: %s", m, strings.Join(services, ", ")))
		}
		return minors[0], true, details, true
	}
	return minors[0], false, nil, true
}

// hasHotfix сравнивает версии сервисов с базовыми тегами релиза.
// Для кор-сервисов сравнивается patch, для остальных — полная версия.
func hasHotfix(latest map[string]tag.Tag, core map[string]bool, base *Baseline) (bool, []string) {
	var (
		hf      bool
		details []string
	)

	for service, t := range latest {
		if core[service] {
			basePatch := 0
			if base != nil {
				if bt, found := base.Tags[service]; found {
					basePatch = bt.Version.Patch
				}
			}
			if t.Version.Patch > basePatch {
				hf = true
			}
			continue
		}

		if base == nil {
			details = append(details, "вне сверки, задача релиза не найдена: "+t.Raw)
			continue
		}
		bt, found := base.Tags[service]
		if !found {
			// Без базового тега сервис не участвует в определении HF.
			details = append(details, "нет в задаче релиза: "+t.Raw)
			continue
		}
		if t.Version.Compare(bt.Version) > 0 {
			hf = true
		}
	}

	return hf, details
}
