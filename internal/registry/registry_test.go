package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// sheetServer подделывает Sheets API: отдаёт заданные строки реестра
// и запоминает пришедший batchUpdate.
type sheetServer struct {
	values  [][]any
	updates *sheets.BatchUpdateValuesRequest
	reads   int
	writes  int
}

func (s *sheetServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/values/"):
			s.reads++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(sheets.ValueRange{Values: s.values})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/values:batchUpdate"):
			s.writes++
			var req sheets.BatchUpdateValuesRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("batchUpdate: тело не разобралось: %v", err)
			}
			s.updates = &req
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(sheets.BatchUpdateValuesResponse{})

		default:
			t.Errorf("неожиданный запрос: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *sheetServer) client(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(context.Background(), Config{
		SpreadsheetID: "copy-id",
		Range:         "A:I",
		Statuses:      []string{"active", "configuring"},
	}, option.WithEndpoint(srv.URL), option.WithHTTPClient(srv.Client()), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("New вернул ошибку: %v", err)
	}
	return c
}

// registryRows повторяет реальный реестр, включая его грабли: дубли
// prod-sz, пустые статусы и archive у среды, которая получает HF.
func registryRows() [][]any {
	return [][]any{
		{"Наименование", "Домен", "Очередь", "Тип", "Размещение", "Статус", "Release", "Менеджер", "Superset"},
		{"prod-qazsu", "qazsu.kz", "1", "prod", "k8s", "active", "release 57/3"},
		{"mod-prod", "mod.kz", "2", "prod", "k8s", "active", "Release 68"},
		{"nit-adilet", "", "", "nit", "k8s", "configuring", ""},
		{"prod-cloud", "", "", "prod", "k8s", "archive", "Release 60"},
		{"prod-sz", "", "", "prod", "k8s", "active", "Release 59"},
		{"prod-sz", "", "", "prod", "k8s", "", ""},
		{"prod-sz", "", "", "prod", "k8s", "", ""},
		{"prod-avtopark", "", "", "prod", "k8s", "", "Release 61"},
	}
}

func TestListFiltersByStatus(t *testing.T) {
	s := &sheetServer{values: registryRows()}
	c := s.client(t, s.start(t))

	rows, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List вернул ошибку: %v", err)
	}

	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	want := []string{"prod-qazsu", "mod-prod", "nit-adilet", "prod-sz"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("среды %v, хочу %v: archive и пустой статус отсеиваются", names, want)
	}
}

func TestListKeepsSheetRowNumbers(t *testing.T) {
	// Номер строки нужен для записи: заголовок занимает первую строку,
	// поэтому prod-qazsu это строка 2, а не 1.
	s := &sheetServer{values: registryRows()}
	c := s.client(t, s.start(t))

	rows, _ := c.List(context.Background())
	if rows[0].Number != 2 {
		t.Errorf("prod-qazsu на строке %d, хочу 2", rows[0].Number)
	}
	if rows[0].Release != "release 57/3" {
		t.Errorf("текущее значение %q, хочу %q", rows[0].Release, "release 57/3")
	}
}

func TestUpdateWritesOnlyChangedCells(t *testing.T) {
	// mod-prod уже стоит на Release 68 - писать нечего.
	s := &sheetServer{values: registryRows()}
	c := s.client(t, s.start(t))
	rows, _ := c.List(context.Background())

	n, err := c.Update(context.Background(), []Update{
		{Row: rows[0], Value: "Release 68 + HF"}, // prod-qazsu: было release 57/3
		{Row: rows[1], Value: "Release 68"},      // mod-prod: не изменилось
	})
	if err != nil {
		t.Fatalf("Update вернул ошибку: %v", err)
	}
	if n != 1 {
		t.Errorf("записано ячеек %d, хочу 1", n)
	}
	if s.writes != 1 {
		t.Errorf("запросов batchUpdate %d, хочу 1 за прогон", s.writes)
	}
	if got := len(s.updates.Data); got != 1 {
		t.Fatalf("диапазонов в batchUpdate %d, хочу 1", got)
	}
	if rng := s.updates.Data[0].Range; rng != "G2" {
		t.Errorf("диапазон %q, хочу G2: трогаем только колонку Release", rng)
	}
}

func TestUpdateSkipsWhenNameMoved(t *testing.T) {
	// Реестр правят руками: строки могли сдвинуться между чтением и записью.
	// Писать по устаревшему номеру строки значило бы затереть чужую среду.
	s := &sheetServer{values: registryRows()}
	c := s.client(t, s.start(t))
	rows, _ := c.List(context.Background())

	// Кто-то вставил строку сверху: prod-qazsu уехал вниз.
	shifted := registryRows()
	shifted[1] = []any{"prod-new", "", "", "prod", "k8s", "active", ""}
	s.values = shifted

	n, err := c.Update(context.Background(), []Update{{Row: rows[0], Value: "Release 68 + HF"}})
	if err != nil {
		t.Fatalf("Update вернул ошибку: %v", err)
	}
	if n != 0 {
		t.Errorf("записано ячеек %d, хочу 0: имя в колонке A не совпало", n)
	}
	if s.writes != 0 {
		t.Errorf("запросов batchUpdate %d, хочу 0", s.writes)
	}
}

func TestUpdateSkipsEmptyValue(t *testing.T) {
	// Релиз не определён - значение в колонке не трогаем.
	s := &sheetServer{values: registryRows()}
	c := s.client(t, s.start(t))
	rows, _ := c.List(context.Background())

	n, err := c.Update(context.Background(), []Update{{Row: rows[0], Value: ""}})
	if err != nil {
		t.Fatalf("Update вернул ошибку: %v", err)
	}
	if n != 0 {
		t.Errorf("записано ячеек %d, хочу 0", n)
	}
	if s.writes != 0 {
		t.Errorf("пустое значение ушло в таблицу")
	}
}

func TestUpdateWithNothingToWriteMakesNoRequest(t *testing.T) {
	s := &sheetServer{values: registryRows()}
	c := s.client(t, s.start(t))

	n, err := c.Update(context.Background(), nil)
	if err != nil {
		t.Fatalf("Update вернул ошибку: %v", err)
	}
	if n != 0 || s.writes != 0 {
		t.Errorf("пустой список дал %d записей и %d запросов, хочу 0 и 0", n, s.writes)
	}
}

func TestUpdateSendsRawValues(t *testing.T) {
	// RAW, а не USER_ENTERED: иначе Sheets истолкует значение по своим
	// правилам, и текст вроде "Release 68" может поехать форматированием.
	s := &sheetServer{values: registryRows()}
	c := s.client(t, s.start(t))
	rows, _ := c.List(context.Background())

	if _, err := c.Update(context.Background(), []Update{{Row: rows[0], Value: "Release 68 + HF"}}); err != nil {
		t.Fatal(err)
	}
	if s.updates.ValueInputOption != "RAW" {
		t.Errorf("ValueInputOption = %q, хочу RAW", s.updates.ValueInputOption)
	}
}
