package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

// EnvOverride — точечные правила по одной среде.
//
// Большинству сред достаточно системного имени из реестра; overrides.yaml
// закрывает исключения, не трогая общие шаблоны ci-tools.
type EnvOverride struct {
	// Environment передаётся в collect-images вместо имени из реестра.
	Environment string
	// TagsName принудительно выбирает раннер.
	TagsName string
	// Skip хранит причину, по которой среду не опрашиваем.
	Skip string
	// Interval опрашивает среду реже раза в час.
	Interval time.Duration
}

// Skipped сообщает, нужно ли пропустить среду.
func (o EnvOverride) Skipped() bool { return o.Skip != "" }

// Overrides — правила по всем средам.
type Overrides struct {
	envs map[string]EnvOverride
}

// For возвращает правила среды. Для среды без правил — пустые,
// чтобы вызывающему не приходилось проверять наличие.
func (o Overrides) For(environment string) EnvOverride {
	return o.envs[environment]
}

// IntervalEnvironments перечисляет по алфавиту среды с правилом interval.
// Время последнего опроса хранится в памяти процесса, поэтому interval действует
// только в режиме -daemon; список нужен, чтобы предупредить об этом при запуске.
func (o Overrides) IntervalEnvironments() []string {
	var envs []string
	for name, ov := range o.envs {
		if ov.Interval > 0 {
			envs = append(envs, name)
		}
	}
	slices.Sort(envs)
	return envs
}

// rawOverrides повторяет формат файла: интервал приходит строкой.
type rawOverrides struct {
	Envs map[string]struct {
		Environment string `yaml:"environment"`
		TagsName    string `yaml:"tags_name"`
		Skip        string `yaml:"skip"`
		Interval    string `yaml:"interval"`
	} `yaml:"envs"`
}

// LoadOverrides читает overrides.yaml.
//
// Отсутствие файла — не ошибка: это штатная установка без особых сред.
func LoadOverrides(path string) (Overrides, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Overrides{envs: map[string]EnvOverride{}}, nil
	}
	if err != nil {
		return Overrides{}, fmt.Errorf("overrides %s: %w", path, err)
	}

	var raw rawOverrides
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Overrides{}, fmt.Errorf("overrides %s: %w", path, err)
	}

	envs := make(map[string]EnvOverride, len(raw.Envs))
	for name, r := range raw.Envs {
		o := EnvOverride{Environment: r.Environment, TagsName: r.TagsName, Skip: r.Skip}
		if r.Interval != "" {
			d, err := time.ParseDuration(r.Interval)
			if err != nil {
				return Overrides{}, fmt.Errorf("overrides %s: среда %s: interval %q не длительность", path, name, r.Interval)
			}
			o.Interval = d
		}
		envs[name] = o
	}

	return Overrides{envs: envs}, nil
}
