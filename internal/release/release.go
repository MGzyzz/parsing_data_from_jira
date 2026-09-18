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
	Number   int
	Hotfixes []map[string]tag.Tag // составы HF после базового релиза для этой среды
	Tags     map[string]tag.Tag   // сервис -> тег из секции Projects
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
	// CoreUnversioned — у кор-сервиса нет тега по схеме релиза (сборка среды,
	// как redo-front:kpo-prod-164897), и релиз посчитан без него.
	CoreUnversioned bool
	JiraMismatch    bool // сверка с задачей релиза не сошлась
	Details         []string
	CollectedAt     time.Time
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

	eligible := make([]tag.Tag, 0, len(state.Tags))
	var ineligible []tag.Tag
	for _, t := range state.Tags {
		if t.Eligible() {
			eligible = append(eligible, t)
		} else {
			ineligible = append(ineligible, t)
		}
	}
	latest := latestByService(eligible)
	core := coreSet(cfg.CoreServices)

	// Пометка о кор-сервисе содержательнее, чем «образ вне схемы»: она говорит,
	// что из расчёта релиза выпал значимый сервис. Тот же образ вторым пунктом
	// не повторяется.
	unversioned := unversionedCore(state, latest, core)
	res.CoreUnversioned = len(unversioned) > 0
	flagged := make(map[string]bool, len(unversioned))
	for _, image := range unversioned {
		flagged[image] = true
		res.Details = append(res.Details, "кор-сервис без версионного тега: "+image)
	}
	for _, image := range state.Unparsed {
		if !flagged[image] {
			res.Details = append(res.Details, "образ вне схемы версий: "+image)
		}
	}
	for _, t := range ineligible {
		if !flagged[t.Raw] {
			res.Details = append(res.Details, "образ вне схемы релиза: "+t.Raw)
		}
	}

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

	// Неподходящие базовые теги не должны скрывать HF реальной среды.
	var valid *Baseline
	if base != nil && base.Number == res.Release {
		valid = &Baseline{Number: base.Number, Tags: map[string]tag.Tag{}, Hotfixes: base.Hotfixes}
		for service, bt := range base.Tags {
			if !bt.Eligible() && !core[service] {
				res.Details = append(res.Details, "базовый тег вне схемы релиза: "+service)
				continue
			}
			if !bt.Eligible() || (core[service] && bt.Version.Minor != minor) {
				res.JiraMismatch = true
				res.Details = append(res.Details, "базовый тег Jira не соответствует релизу: "+service)
				continue
			}
			valid.Tags[service] = bt
		}
		for service := range latest {
			if core[service] {
				if _, ok := valid.Tags[service]; !ok {
					res.JiraMismatch = true
					res.Details = append(res.Details, "нет корректной базы кор-сервиса: "+service)
				}
			}
		}
	} else {
		res.Details = append(res.Details, "база Jira для релиза не найдена")
	}
	hf, hfDetails := hasHotfix(latest, core, valid)
	if valid != nil {
		for service, t := range latest {
			bt, found := valid.Tags[service]
			if !found {
				continue
			}
			newer := t.Version.Compare(bt.Version) > 0
			if core[service] {
				newer = t.Version.Patch > bt.Version.Patch
			}
			if !newer {
				continue
			}
			confirmed := false
			for _, tags := range valid.Hotfixes {
				ht, ok := tags[service]
				if ok && ht.Eligible() && ht.Branch == t.Branch && ht.Version == t.Version {
					confirmed = true
					break
				}
			}
			if !confirmed {
				res.JiraMismatch = true
				res.Details = append(res.Details, "тег не подтверждён HF после релиза: "+t.Raw)
			}
		}
	}
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

// unversionedCore перечисляет образы кор-сервисов, у которых нет ни одного
// тега по схеме релиза. Во время накатки со сборки среды на версию
// версионный тег уже есть, и сервис в расчёте участвует — это не пометка.
// Возвращаются сами образы: вызывающий код по ним же отсеивает дубли в деталях.
func unversionedCore(state EnvState, latest map[string]tag.Tag, core map[string]bool) []string {
	var details []string
	check := func(service, image string) {
		if _, versioned := latest[service]; core[service] && !versioned {
			details = append(details, image)
		}
	}
	for _, image := range state.Unparsed {
		check(tag.ImageService(image), image)
	}
	for _, t := range state.Tags {
		if !t.Eligible() {
			check(t.Service, t.Raw)
		}
	}
	return details
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
