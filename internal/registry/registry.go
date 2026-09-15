// Package registry читает реестр сред из Google Sheets и проставляет
// в нём колонку Release.
//
// Реестр — рабочая таблица команды, её правят руками. Отсюда правила записи:
// трогаем только колонку G, пишем лишь изменившиеся ячейки и перед записью
// сверяем имя среды в колонке A.
package registry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// Колонки реестра, считая от начала диапазона (A:I).
const (
	colName    = 0 // A, Наименование
	colStatus  = 5 // F, Статус
	colRelease = 6 // G, Release

	releaseColumn = "G"
)

// Config — параметры подключения к реестру.
type Config struct {
	SpreadsheetID string
	Range         string
	Statuses      []string     // строки с другим статусом не обрабатываются
	Logger        *slog.Logger // nil = slog.Default()
}

// Row — строка реестра, которую сервис обрабатывает.
type Row struct {
	Number  int // номер строки в таблице, с единицы; по нему идёт запись
	Name    string
	Status  string
	Release string
}

// Update — новое значение колонки Release для строки.
type Update struct {
	Row   Row
	Value string
}

type Client struct {
	cfg    Config
	log    *slog.Logger
	sheets *sheets.Service
}

// New поднимает клиента Sheets. Авторизацию передаёт вызывающий через opts:
// пакету незачем знать, откуда берётся ключ service account.
func New(ctx context.Context, cfg Config, opts ...option.ClientOption) (*Client, error) {
	srv, err := sheets.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("клиент Sheets: %w", err)
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Client{cfg: cfg, log: log, sheets: srv}, nil
}

// List возвращает строки реестра с нужным статусом.
//
// Дубли среды не склеиваются: у лишних строк статус пустой, и фильтр
// по статусу отсеивает их сам.
func (c *Client) List(ctx context.Context) ([]Row, error) {
	values, err := c.read(ctx)
	if err != nil {
		return nil, err
	}

	var rows []Row
	// Первая строка — заголовок.
	for i := 1; i < len(values); i++ {
		name, status := cell(values[i], colName), cell(values[i], colStatus)
		// Без имени среду не опросить, а сверка перед записью теряет смысл.
		if name == "" || !c.wanted(status) {
			continue
		}
		rows = append(rows, Row{
			Number:  i + 1,
			Name:    name,
			Status:  status,
			Release: cell(values[i], colRelease),
		})
	}
	return rows, nil
}

// Update записывает изменившиеся значения одним batchUpdate и возвращает
// число записанных ячеек.
//
// Номера строк из List могли устареть: между чтением и записью проходит
// опрос всех сред, а таблицу в это время правят руками. Поэтому реестр
// перечитывается, и запись идёт только туда, где имя в колонке A совпало.
// Иначе вставленная сверху строка сдвинула бы запись на чужую среду.
func (c *Client) Update(ctx context.Context, updates []Update) (int, error) {
	pending := make([]Update, 0, len(updates))
	for _, u := range updates {
		// Пустое значение — релиз не определён. Старое значение лучше, чем никакое.
		if u.Value != "" {
			pending = append(pending, u)
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}

	values, err := c.read(ctx)
	if err != nil {
		return 0, err
	}

	var data []*sheets.ValueRange
	for _, u := range pending {
		current := rowAt(values, u.Row.Number)
		if name := cell(current, colName); name != u.Row.Name {
			c.log.Warn("строка реестра сдвинулась, запись пропущена",
				"env", u.Row.Name, "row", u.Row.Number, "found", name)
			continue
		}
		if cell(current, colRelease) == u.Value {
			continue
		}
		data = append(data, &sheets.ValueRange{
			Range:  fmt.Sprintf("%s%d", releaseColumn, u.Row.Number),
			Values: [][]any{{u.Value}},
		})
	}
	if len(data) == 0 {
		return 0, nil
	}

	_, err = c.sheets.Spreadsheets.Values.BatchUpdate(c.cfg.SpreadsheetID, &sheets.BatchUpdateValuesRequest{
		// RAW: значение ложится как текст. USER_ENTERED толковал бы его
		// по правилам Sheets, и строка, начатая с "=", стала бы формулой.
		ValueInputOption: "RAW",
		Data:             data,
	}).Context(ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("записать колонку Release: %w", err)
	}
	return len(data), nil
}

func (c *Client) read(ctx context.Context) ([][]any, error) {
	resp, err := c.sheets.Spreadsheets.Values.Get(c.cfg.SpreadsheetID, c.cfg.Range).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("прочитать реестр: %w", err)
	}
	return resp.Values, nil
}

// wanted сверяет статус без учёта регистра и пробелов: колонку заполняют руками.
func (c *Client) wanted(status string) bool {
	for _, s := range c.cfg.Statuses {
		if strings.EqualFold(status, s) {
			return true
		}
	}
	return false
}

// rowAt возвращает строку таблицы по её номеру. Sheets не присылает
// хвостовые пустые строки, поэтому номер может оказаться за концом ответа.
func rowAt(values [][]any, number int) []any {
	if number < 1 || number > len(values) {
		return nil
	}
	return values[number-1]
}

// cell возвращает значение ячейки. Sheets обрезает хвостовые пустые ячейки
// строки, так что короткая строка — норма, а не ошибка.
func cell(row []any, col int) string {
	if col >= len(row) {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(row[col]))
}
