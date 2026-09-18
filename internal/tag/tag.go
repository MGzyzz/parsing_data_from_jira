// Package tag разбирает и сравнивает версии образов, нормализует имена сервисов.
package tag

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrNotVersionTag означает, что тег не похож на версию: образ собран из
// ветки или это инфраструктурный образ. Такие в расчёт релиза не идут.
var ErrNotVersionTag = errors.New("тег не является версией")

// Version содержит числовые компоненты версии и суффикс сборки.
type Version struct {
	Major, Minor, Patch int
	Suffix              string
}

// Tag — сервис и его версия, независимо от источника.
type Tag struct {
	Service string // имя образа: redo-backend
	Branch  string // main, release или пусто
	Version Version
	Raw     string // исходная строка — для логов и разбора инцидентов
}

// versionTag разбирает часть тега после двоеточия.
// Ветка необязательна: стороннее ПО ставится тегом вида 8.3.3.
var versionTag = regexp.MustCompile(`^(?:([a-z]+)-)?(\d+)\.(\d+)\.(\d+)(?:-(.+))?$`)

// ParseImage разбирает ссылку на образ вида "redo-backend:main-1.29.13".
func ParseImage(image string) (Tag, error) {
	name, version, ok := splitImage(image)
	if !ok || name == "" {
		return Tag{}, fmt.Errorf("%q: нет тега: %w", image, ErrNotVersionTag)
	}
	v, branch, err := parseVersion(version)
	if err != nil {
		return Tag{}, fmt.Errorf("%q: %w", image, err)
	}

	return Tag{Service: name, Branch: branch, Version: v, Raw: image}, nil
}

// ImageService возвращает имя сервиса из ссылки на образ, даже если тег
// не по схеме версий: кор-сервис со сборкой среды надо узнать по имени.
func ImageService(image string) string {
	name, _, _ := splitImage(image)
	return name
}

// splitImage отделяет имя образа от тега, отбрасывая адрес реестра и дайджест.
// Двоеточие порта реестра стоит до последнего слеша и в имя не попадает.
func splitImage(image string) (name, version string, ok bool) {
	reference, _, _ := strings.Cut(image, "@")
	return strings.Cut(reference[strings.LastIndex(reference, "/")+1:], ":")
}

// parseVersion разбирает "main-1.29.13" на версию и ветку.
func parseVersion(s string) (Version, string, error) {
	m := versionTag.FindStringSubmatch(s)
	if m == nil {
		return Version{}, "", ErrNotVersionTag
	}

	major, e1 := strconv.Atoi(m[2])
	minor, e2 := strconv.Atoi(m[3])
	patch, e3 := strconv.Atoi(m[4])
	if e1 != nil || e2 != nil || e3 != nil {
		return Version{}, "", ErrNotVersionTag
	}

	return Version{Major: major, Minor: minor, Patch: patch, Suffix: m[5]}, m[1], nil
}

// Compare сравнивает версии: -1 если v младше o, 0 если равны, 1 если старше.
//
// Числа сравниваются как числа: строкой "1.29.9" оказалась бы старше
// "1.29.13", и признак HF определялся бы неверно.
func (v Version) Compare(o Version) int {
	for _, p := range [][2]int{
		{v.Major, o.Major},
		{v.Minor, o.Minor},
		{v.Patch, o.Patch},
	} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}

	// Cherry-pick собран поверх обычного тега, значит новее его.
	return strings.Compare(v.Suffix, o.Suffix)
}

// listMarker убирает маркер списка в начале строки: вики-разметка Jira даёт
// " # ", в присланных вручную выгрузках встречается "3. " и "- ".
// Пробел после дефиса обязателен: без него дефис начинает зачёркивание
// (-enbek-integration: main-1.29.0-), а не список.
var listMarker = regexp.MustCompile(`^(?:#+|\d+\.|-\s)\s*`)

// nonRedoServices перечисляет сервисы, имена которых не требуют префикса redo-.
var nonRedoServices = map[string]bool{
	"onlyoffice-documentserver-unlimited": true,
}

// NormalizeService добавляет префикс redo-, кроме сервисов из nonRedoServices.
func NormalizeService(name string) string {
	// В задачах встречается redo_front через подчёркивание.
	name = strings.ReplaceAll(name, "_", "-")
	if strings.HasPrefix(name, "redo-") || nonRedoServices[name] {
		return name
	}
	return "redo-" + name
}

// ParseProjectLine разбирает строку секции Projects из описания Jira.
// Возвращает ok=false без ошибки для пропускаемых строк: например,
// зачёркнутого сервиса или пустого тега. Нераспознаваемый тег возвращает ошибку.
func ParseProjectLine(line string) (Tag, bool, error) {
	raw := listMarker.ReplaceAllString(strings.TrimSpace(line), "")
	if raw == "" {
		return Tag{}, false, nil
	}

	// Зачёркнутый сервис исключён из релиза: -enbek-integration: main-1.29.0-.
	// Закрывающий дефис ищем не в конце строки: за ним нередко идёт пояснение
	// вроде «накатывать не нужно».
	if strings.HasPrefix(raw, "-") {
		return Tag{}, false, nil
	}

	// Разметку снимаем только для разбора: в Raw строка остаётся как в задаче.
	name, rest, ok := strings.Cut(stripMarkup(raw), ":")
	if !ok {
		return Tag{}, false, nil
	}

	// Хвост-комментарий после тега отбрасываем, берём первый токен.
	fields := strings.Fields(rest)

	// Зачёркнутый тег исключён. Если рядом поставили новый — берём его:
	// redo_front: -release-1.28.1- [release-1.28.2|https://...]
	var struck bool
	for len(fields) > 0 && struckThrough(fields[0]) {
		struck = true
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return Tag{}, false, nil // сервис упомянут, но тег не проставлен
	}

	v, branch, err := parseVersion(versionToken(fields[0]))
	if err != nil {
		// Тег зачеркнули, а замены не оказалось: сервис исключён из релиза,
		// это решение автора задачи, а не испорченные данные.
		if struck {
			return Tag{}, false, nil
		}
		return Tag{}, false, fmt.Errorf("%q: %w", raw, err)
	}

	return Tag{
		Service: NormalizeService(strings.TrimSpace(name)),
		Branch:  branch,
		Version: v,
		Raw:     raw,
	}, true, nil
}

// struckThrough распознаёт зачёркнутый токен Jira: -main-1.1.0-.
func struckThrough(token string) bool {
	return len(token) > 2 && strings.HasPrefix(token, "-") && strings.HasSuffix(token, "-")
}

// colorMarkup — цветовая разметка Jira {color:#172b4d}...{color}. Появляется,
// когда состав копируют из другого редактора; в двоеточии внутри неё
// строка сервиса разрезалась бы не там.
var colorMarkup = regexp.MustCompile(`\{color(?::[^}]*)?\}`)

// stripMarkup снимает жирный шрифт и цвет, оставляя текст строки.
func stripMarkup(s string) string {
	return colorMarkup.ReplaceAllString(strings.ReplaceAll(s, "*", ""), "")
}

// versionToken достаёт тег из первого токена значения: без вики-ссылки
// и без комментария в скобках, приписанного без пробела: release-1.24.1(только ...).
func versionToken(field string) string {
	token, _, _ := strings.Cut(linkText(field), "(")
	return token
}

// linkText снимает вики-ссылку Jira с тега: [main-1.29.17|https://gitlab/...].
// Адрес ссылки пробелов не содержит, поэтому ссылка целиком — первый токен.
func linkText(token string) string {
	if !strings.HasPrefix(token, "[") {
		return token
	}
	text, _, _ := strings.Cut(strings.TrimPrefix(token, "["), "|")
	return strings.TrimSuffix(text, "]")
}

// Eligible сообщает, относится ли тег к схеме релизов из ТЗ.
func (t Tag) Eligible() bool {
	return t.Version.Major == 1 && (t.Branch == "main" || t.Branch == "release")
}
