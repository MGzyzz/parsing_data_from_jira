package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// googleClientOption выбирает способ авторизации по содержимому файла
// credentials. Подсказка в ошибке важна: на сервере путь через личный OAuth
// не завершить, и человек должен узнать об этом из сообщения, а не опытным путём.
func TestGoogleClientOptionByFileType(t *testing.T) {
	dir := t.TempDir()
	// Токен ищется относительно рабочего каталога — уводим его от репозитория.
	t.Chdir(dir)

	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	serviceAccount := write("sa.json", `{"type":"service_account","project_id":"p","client_email":"a@p.iam.gserviceaccount.com","token_uri":"https://oauth2.googleapis.com/token"}`)
	if _, err := googleClientOption(t.Context(), serviceAccount); err != nil {
		t.Fatalf("ключ service account отклонён: %v", err)
	}

	installed := write("client.json", `{"installed":{"client_id":"id","client_secret":"s","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","redirect_uris":["http://localhost"]}}`)
	_, err := googleClientOption(t.Context(), installed)
	if err == nil {
		t.Fatal("OAuth без сохранённого токена принят")
	}
	if !strings.Contains(err.Error(), "service account") {
		t.Errorf("ошибка не подсказывает про service account, хотя на сервере другого пути нет: %v", err)
	}

	junk := write("junk.json", `{"whatever":1}`)
	if _, err := googleClientOption(t.Context(), junk); err == nil {
		t.Error("посторонний JSON принят за credentials")
	}

	if _, err := googleClientOption(t.Context(), filepath.Join(dir, "missing.json")); err == nil {
		t.Error("отсутствующий файл принят")
	}
}
