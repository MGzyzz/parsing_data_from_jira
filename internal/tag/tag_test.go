package tag

import "testing"

func TestParseImage(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Tag
		wantErr bool
	}{
		{
			name: "обычный сервис на main",
			in:   "redo-backend:main-1.29.13",
			want: Tag{Service: "redo-backend", Branch: "main", Version: Version{1, 29, 13, ""}, Raw: "redo-backend:main-1.29.13"},
		},
		{
			name: "ветка release вместо main",
			in:   "redo-keycloak:release-1.21.5",
			want: Tag{Service: "redo-keycloak", Branch: "release", Version: Version{1, 21, 5, ""}, Raw: "redo-keycloak:release-1.21.5"},
		},
		{
			// Стороннее ПО в namespace: ветки в теге нет вообще.
			name: "тег без ветки",
			in:   "onlyoffice-documentserver-unlimited:8.3.3",
			want: Tag{Service: "onlyoffice-documentserver-unlimited", Branch: "", Version: Version{8, 3, 3, ""}, Raw: "onlyoffice-documentserver-unlimited:8.3.3"},
		},
		{
			// Cherry-pick поверх релиза: хвост после патча надо сохранить,
			// иначе два разных образа станут неотличимы.
			name: "cherry-pick в суффиксе",
			in:   "redo-backend:main-1.28.1-mcp4-8164",
			want: Tag{Service: "redo-backend", Branch: "main", Version: Version{1, 28, 1, "mcp4-8164"}, Raw: "redo-backend:main-1.28.1-mcp4-8164"},
		},
		{
			name:    "образ без тега",
			in:      "busybox",
			wantErr: true,
		},
		{
			// Теги веток с офисных сред под схему не подходят и в расчёт не идут.
			name:    "тег ветки вместо версии",
			in:      "redo-backend:feature-DOPS-1234",
			wantErr: true,
		},
		{
			name:    "пустая строка",
			in:      "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseImage(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseImage(%q) = %+v, ожидалась ошибка", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseImage(%q) вернул ошибку: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseImage(%q)\n  = %+v\nхочу %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		name string
		a, b Version
		want int
	}{
		{
			// Главная ловушка: как строки "1.29.9" > "1.29.13", как версии — наоборот.
			name: "патч сравнивается числом, а не строкой",
			a:    Version{1, 29, 13, ""},
			b:    Version{1, 29, 9, ""},
			want: 1,
		},
		{
			name: "minor старше патча",
			a:    Version{1, 30, 0, ""},
			b:    Version{1, 29, 20, ""},
			want: 1,
		},
		{
			name: "равные версии",
			a:    Version{1, 29, 0, ""},
			b:    Version{1, 29, 0, ""},
			want: 0,
		},
		{
			// Cherry-pick собран поверх обычного тега, значит новее.
			name: "суффикс новее чистой версии",
			a:    Version{1, 28, 1, "mcp4-8164"},
			b:    Version{1, 28, 1, ""},
			want: 1,
		},
		{
			name: "меньшая версия",
			a:    Version{1, 20, 0, ""},
			b:    Version{1, 29, 0, ""},
			want: -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Compare(tc.b); got != tc.want {
				t.Errorf("%+v.Compare(%+v) = %d, хочу %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestParseProjectLine проверяет разбор секции Projects из описания задачи Jira.
// Все случаи взяты из реальных задач: DOPS-4820 (Release 68) и DOPS-4892 (HF).
func TestParseProjectLine(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		want     Tag
		wantSkip bool // строку намеренно пропускаем, это не ошибка
		wantErr  bool
	}{
		{
			name: "короткое имя нормализуется к имени образа",
			in:   " # backend: main-1.29.0",
			want: Tag{Service: "redo-backend", Branch: "main", Version: Version{1, 29, 0, ""}, Raw: "backend: main-1.29.0"},
		},
		{
			name: "полное имя остаётся как есть",
			in:   " # redo-email: main-1.29.0",
			want: Tag{Service: "redo-email", Branch: "main", Version: Version{1, 29, 0, ""}, Raw: "redo-email: main-1.29.0"},
		},
		{
			// Заказчик присылал состав среды нумерованным списком, а не вики-разметкой.
			name: "нумерованный список вместо решётки",
			in:   "3. redo-camunda: main-1.26.2",
			want: Tag{Service: "redo-camunda", Branch: "main", Version: Version{1, 26, 2, ""}, Raw: "redo-camunda: main-1.26.2"},
		},
		{
			name: "стороннее ПО без префикса redo",
			in:   "16. onlyoffice-documentserver-unlimited: 8.3.3",
			want: Tag{Service: "onlyoffice-documentserver-unlimited", Branch: "", Version: Version{8, 3, 3, ""}, Raw: "onlyoffice-documentserver-unlimited: 8.3.3"},
		},
		{
			name: "жирная разметка снимается",
			in:   " # *front*: main-1.29.0",
			want: Tag{Service: "redo-front", Branch: "main", Version: Version{1, 29, 0, ""}, Raw: "*front*: main-1.29.0"},
		},
		{
			// После тега пишут пояснения: берём только первый токен.
			name: "хвост-комментарий после тега",
			in:   " # backend: main-1.29.13 откатили и накатили заново",
			want: Tag{Service: "redo-backend", Branch: "main", Version: Version{1, 29, 13, ""}, Raw: "backend: main-1.29.13 откатили и накатили заново"},
		},
		{
			// Зачёркнутый сервис исключён из релиза.
			name:     "зачёркнутая строка пропускается",
			in:       " # -enbek-integration: main-1.29.0-",
			wantSkip: true,
		},
		{
			name:     "пустой тег пропускается",
			in:       " # esedo-gateway:",
			wantSkip: true,
		},
		{
			name:     "заголовок секции не строка сервиса",
			in:       "*Projects:*",
			wantSkip: true,
		},
		{
			name:     "пустая строка пропускается",
			in:       "   ",
			wantSkip: true,
		},
		{
			// Строка похожа на сервис, но тег не версия: это стоит увидеть в логе,
			// а не проглотить молча.
			name:    "тег не версия — ошибка, а не пропуск",
			in:      " # backend: feature-DOPS-1234",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := ParseProjectLine(tc.in)

			switch {
			case tc.wantErr:
				if err == nil {
					t.Fatalf("ParseProjectLine(%q) = %+v, ok=%v; ожидалась ошибка", tc.in, got, ok)
				}
				return
			case tc.wantSkip:
				if err != nil {
					t.Fatalf("ParseProjectLine(%q) вернул ошибку %v; ожидался пропуск", tc.in, err)
				}
				if ok {
					t.Fatalf("ParseProjectLine(%q) = %+v; ожидался пропуск", tc.in, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseProjectLine(%q) вернул ошибку: %v", tc.in, err)
			}
			if !ok {
				t.Fatalf("ParseProjectLine(%q) пропустил строку, ожидался разбор", tc.in)
			}
			if got != tc.want {
				t.Errorf("ParseProjectLine(%q)\n  = %+v\nхочу %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeService(t *testing.T) {
	tests := []struct{ in, want string }{
		{"backend", "redo-backend"},
		{"nuxeo", "redo-nuxeo"},
		{"camunda", "redo-camunda"},
		{"front", "redo-front"},
		{"integration", "redo-integration"},
		// Составные имена тоже получают префикс: в кластере образ зовётся
		// redo-report-data, а в Jira пишут просто report-data.
		{"report-data", "redo-report-data"},
		{"enbek-integration", "redo-enbek-integration"},
		// Уже полное имя не удваивается.
		{"redo-backend", "redo-backend"},
		{"redo-email", "redo-email"},
		// Стороннее ПО префикс не получает: такого образа в кластере нет.
		{"onlyoffice-documentserver-unlimited", "onlyoffice-documentserver-unlimited"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizeService(tc.in); got != tc.want {
				t.Errorf("NormalizeService(%q) = %q, хочу %q", tc.in, got, tc.want)
			}
		})
	}
}
