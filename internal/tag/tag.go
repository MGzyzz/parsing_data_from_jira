// Package tag разбирает теги образов и приводит имена сервисов к одному виду.
//
// Теги приходят из двух источников в разных формах: из кластера — как
// "redo-backend:main-1.29.13", из секции Projects в Jira — как
// " # backend: main-1.29.0". Смысл у них один, поэтому разбор и сравнение
// живут здесь: если правила разъедутся по двум местам, сервис начнёт врать
// молча, а расхождение всплывёт уже на боевых средах.
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

// Version — версия из тега.
//
// Suffix хранит хвост cherry-pick (main-1.28.1-mcp4-8164): без него два
// разных образа выглядели бы одинаково.
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
	name, version, ok := strings.Cut(image, ":")
	if !ok || name == "" {
		return Tag{}, fmt.Errorf("%q: нет тега: %w", image, ErrNotVersionTag)
	}

	v, branch, err := parseVersion(version)
	if err != nil {
		return Tag{}, fmt.Errorf("%q: %w", image, err)
	}

	return Tag{Service: name, Branch: branch, Version: v, Raw: image}, nil
}

// parseVersion разбирает "main-1.29.13" на версию и ветку.
func parseVersion(s string) (Version, string, error) {
	m := versionTag.FindStringSubmatch(s)
	if m == nil {
		return Version{}, "", ErrNotVersionTag
	}

	// Ошибку Atoi не проверяем: регулярное выражение уже гарантирует цифры.
	major, _ := strconv.Atoi(m[2])
	minor, _ := strconv.Atoi(m[3])
	patch, _ := strconv.Atoi(m[4])

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
// " # ", а в присланных вручную выгрузках встречается "3. ".
var listMarker = regexp.MustCompile(`^(?:#+|\d+\.)\s*`)

// nonRedoServices — образы, которым префикс redo- не положен: стороннее ПО,
// живущее в тех же namespace. Список дополняется по мере встречи таких образов.
var nonRedoServices = map[string]bool{
	"onlyoffice-documentserver-unlimited": true,
}

// NormalizeService приводит имя сервиса к имени образа в кластере.
//
// В Jira пишут то "backend:", то "redo-backend:" — без приведения к одному
// виду сверка с тегами кластера не сойдётся и все сервисы попадут в детали.
func NormalizeService(name string) string {
	if strings.HasPrefix(name, "redo-") || nonRedoServices[name] {
		return name
	}
	return "redo-" + name
}

// ParseProjectLine разбирает строку секции Projects из описания задачи Jira.
//
// Второе возвращаемое значение отличает намеренный пропуск от ошибки:
// зачёркнутый сервис, пустой тег и заголовок секции — это не сбой разбора,
// а часть формата. Ошибка означает, что строка похожа на сервис, но тег
// прочитать не удалось: такое должно попасть в лог, а не потеряться.
func ParseProjectLine(line string) (Tag, bool, error) {
	raw := listMarker.ReplaceAllString(strings.TrimSpace(line), "")
	if raw == "" {
		return Tag{}, false, nil
	}

	// Зачёркнутый сервис исключён из релиза: -enbek-integration: main-1.29.0-
	if strings.HasPrefix(raw, "-") && strings.HasSuffix(raw, "-") {
		return Tag{}, false, nil
	}

	// Разметку снимаем только для разбора: в Raw строка остаётся как в задаче.
	name, rest, ok := strings.Cut(strings.ReplaceAll(raw, "*", ""), ":")
	if !ok {
		return Tag{}, false, nil
	}

	// Хвост-комментарий после тега отбрасываем, берём первый токен.
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return Tag{}, false, nil // сервис упомянут, но тег не проставлен
	}

	v, branch, err := parseVersion(fields[0])
	if err != nil {
		return Tag{}, false, fmt.Errorf("%q: %w", raw, err)
	}

	return Tag{
		Service: NormalizeService(strings.TrimSpace(name)),
		Branch:  branch,
		Version: v,
		Raw:     raw,
	}, true, nil
}
