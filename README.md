# env-release-tracker

Сервис на Go для определения релизов сред по версиям контейнерных образов
и обновления колонки `Release` в Google Sheets. Номер релиза рассчитывается
по тегам основных сервисов; базовые версии из Jira используются для определения HF.

## Текущее состояние

Сервис читает Google Sheets и Jira. Режим `IMAGE_COLLECTOR=stub` (по умолчанию)
читает встроенные файлы `internal/gitlab/testdata/*.json`.
Режим `IMAGE_COLLECTOR=gitlab` запускает пайплайн collect-images через GitLab API,
ожидает завершения и получает `images.json` из джобы этого пайплайна.
Если артефакт отсутствует (HTTP 404), используется список образов из лога джобы.

В режиме stub и при подключении проекта имитации результат основан на тестовых
образах и не подтверждает фактическую версию среды. Для проверки используйте копию таблицы. По умолчанию запись отключена.

Релиз считается только по тегам `main-1.minor.patch` и `release-1.minor.patch`.
Полные адреса образов с registry/портом поддерживаются; образы только с digest
остаются в диагностике. База и HF берутся из завершённых задач Jira
(`statusCategory.key=done`). Неподтверждённый HF или неверная база дают предупреждение,
но не блокируют запись вычисленного значения.

Ограничения:

- Интервалы опроса хранятся в памяти и сбрасываются при перезапуске процесса.
- При нескольких задачах одного релиза выбирается последняя по дате события;
  при равных датах — по ключу задачи. HF подтверждает точный тег и относится к
  той же среде; его дата должна быть позже даты базы. Неполные данные дают расхождение.
- `tags_name` передаётся в pipeline как одноимённая переменная. Целевой шаблон
  должен использовать её для выбора раннера; это необходимо проверить на своей
  инсталляции GitLab. Stub переопределение раннера не поддерживает.

## Первый запуск

Нужны Go 1.27+, доступ к Jira и Google Sheets. Команды выполняются из корня проекта.
Для проверки используйте копию таблицы со вкладкой `gid=0`: строка 1 — заголовки,
A — имя среды, F — статус, G — релиз. Другие диапазоны пока не поддерживаются.

1. В Google Cloud включите Google Sheets API, создайте service account и получите
   JSON-ключ. Сохраните его в `secrets/service-account.json`. Дайте этой учётке доступ
   к копии таблицы: чтение для dry-run, редактирование для записи.
2. Создайте локальный файл `env.sh` по примеру ниже и заполните значения в редакторе.
   Этот файл исключён из Git. Токены не вставляйте в команды терминала или комментарии.

   ```bash
   export JIRA_URL='https://jira.example.org'
   export JIRA_TOKEN=''
   export JIRA_JQL='summary ~ "Release" AND project = DevOps'
   export SHEET_ID=''
   export SHEET_RANGE='A:I'
   export GOOGLE_CREDENTIALS_JSON='./secrets/service-account.json'
   export IMAGE_COLLECTOR='stub'
   ```

3. Загрузите настройки и проверьте одну среду:

   ```bash
   chmod 600 env.sh secrets/service-account.json
   source ./env.sh
   go test ./...
   go run . -env=prod-qazsu
   ```

   Для этого примера в копии реестра должна быть строка `prod-qazsu` со статусом
   `active` или `configuring`: stub использует встроенный пример этой среды.
   Stub всё равно читает Jira и Sheets. Ожидаемый лог: `релиз определён`, затем
   `dry-run: запись пропущена`. Предупреждение Jira означает, что тестовые теги
   не подтверждены вашими задачами.
4. Для сбора реальных образов добавьте в `env.sh` параметры вашего collect-images,
   повторно выполните `source ./env.sh` и запустите одну среду без `-write`:

   ```bash
   export IMAGE_COLLECTOR='gitlab'
   export GITLAB_URL='https://gitlab.example.org'
   export GITLAB_TOKEN=''
   export COLLECT_IMAGES_PROJECT_ID='234'
   export COLLECT_IMAGES_REF='main'
   ```

   Нужен токен с доступом к запуску pipeline и чтению jobs, artifacts и trace.
   После проверки результата добавьте `-write`, чтобы обновить колонку G копии таблицы.

Получение ключей: [инструкция Google](https://developers.google.com/workspace/guides/create-credentials).

## Сборка и тесты

Версия Go в `go.mod` — `1.27.0`.

```bash
go build -o env-release-tracker .
go test ./...
```

Для проверки гонок данных:

```bash
go test -race ./...
```

## Конфигурация

Настройки читаются из переменных окружения. Список параметров и значений
по умолчанию приведён в [.env.example](.env.example).
**Приложение не загружает `.env` автоматически.** При локальном запуске
передайте переменные через оболочку или настройки запуска IDE.

Обязательные параметры: `JIRA_URL`, `JIRA_TOKEN`, `SHEET_ID`,
`GOOGLE_CREDENTIALS_JSON`. В режиме `gitlab` дополнительно обязательны
`GITLAB_URL`, `GITLAB_TOKEN` и положительный `COLLECT_IMAGES_PROJECT_ID`.

`SHEET_ID` должен указывать на копию таблицы для разработки.

Пример задания JQL в bash/zsh:

```bash
export JIRA_JQL='summary ~ "Release" AND project = DevOps'
```

Файл [overrides.example.yaml](overrides.example.yaml) содержит примеры исключений
для сред. При необходимости создайте на его основе `overrides.yaml` и настройте
пропуски, имена сред и интервалы. Отсутствие файла допустимо.

### Google Sheets

`GOOGLE_CREDENTIALS_JSON` задаёт **путь к файлу credentials**, а не JSON-строку.
Тип авторизации определяется по содержимому файла:

- **Service account:** используется файл ключа. Google Sheets API должен быть
  включён в проекте Google Cloud, а таблица предоставлена технической учётке
  для чтения или редактирования в зависимости от режима запуска.
- **OAuth installed-app:** используется вход пользователя через браузер.
  В Google Cloud настройте OAuth consent screen, при необходимости добавьте
  своего пользователя в Test users и создайте OAuth client типа Desktop app.
  Скачайте JSON в `secrets/client_secret.json` и задайте
  `GOOGLE_CREDENTIALS_JSON=./secrets/client_secret.json`. Затем выполните команду локально:

  ```bash
  mkdir -p secrets
  go run ./cmd/googleauth
  ```

  Команда сохраняет токен в `secrets/token.json`. Основной сервис читает его
  по этому пути относительно рабочего каталога. Библиотека обновляет токен
  доступа при наличии действующего refresh token; обновлённый токен на диск
  основной сервис не записывает.

Для service account браузерный вход и `secrets/token.json` не нужны.
Стандартные пути секретов исключены из Git.
Не размещайте credentials в произвольных файлах или комментариях.

### Jira

`JIRA_TOKEN` — Personal Access Token экземпляра Jira Server / Data Center.
Клиент передаёт его как Bearer-токен только настроенному origin.
Перенаправление на другой origin запрещено. Используйте HTTPS.
Один HTTP-запрос ограничен 30 секундами, вся загрузка Jira — 2 минутами. Для серверного запуска используйте
техническую учётку. Транспорт ограничивает запросы разрешёнными операциями чтения.

## Запуск

После настройки окружения запуск выполняется из корня проекта:

```bash
go run .                        # один цикл без записи
go run . -env=prod-qazsu         # проверка одной среды без записи
go run . -daemon                # первый цикл сразу, затем раз в час
go run . -config=./overrides.yaml
```

Для записи результата тестовых образов в копию таблицы:

```bash
go run . -env=prod-qazsu -write
```

| Флаг | Назначение |
|------|------------|
| `-write` | Разрешить запись в колонку Release |
| `-env <имя>` | Обработать одну среду из реестра |
| `-daemon` | Повторять обработку раз в час в одном процессе |
| `-config <путь>` | Путь к overrides; по умолчанию используется `OVERRIDES_FILE` |

Без `-daemon` программа выполняет один цикл и завершается. Для периодического
запуска можно использовать внешний планировщик, например Kubernetes CronJob.
При отдельных запусках настройка `interval` не сохраняется между процессами.

## Структура проекта

```text
main.go              флаги, создание зависимостей, расписание и сигналы
internal/
  app/                последовательность обработки, параллельный сбор
  config/             переменные окружения и overrides.yaml
  tag/                разбор и сравнение версий, нормализация имён
  release/            расчёт номера релиза и признака HF
  jira/               чтение и разбор задач Release/HF
  gitlab/             API-клиент collect-images и локальная заглушка
  registry/           чтение и обновление Google Sheets
cmd/googleauth/       первоначальный OAuth-вход пользователя
```

Оба сборщика реализуют интерфейс `app.Collector`; выбор задаётся `IMAGE_COLLECTOR`.

## Проверка через тестовый GitLab

Сначала создайте Personal Access Token со scope `api` и сохраните его
локально как `GITLAB_TOKEN`. SSH-ключ для API не используется.
После загрузки остальных настроек окружения задайте:

```bash
export IMAGE_COLLECTOR=gitlab
export GITLAB_URL=https://gitlab.com
export COLLECT_IMAGES_PROJECT_ID=86532070
export COLLECT_IMAGES_REF=main
export GITLAB_MOCK_SCENARIO=hotfix

go run . -env=prod-holding
```

Это запускает пайплайн проекта `Gzyzz/kubernetstest` и выполняет расчёт без
записи в Sheets. Dry-run отключает только запись в таблицу: при режиме `gitlab`
пайплайн запускается даже без `-write`.

`GITLAB_POLL_INTERVAL` задаёт интервал проверки (по умолчанию `3s`),
`PIPELINE_TIMEOUT` — предельное время сбора (`10m`). При таймауте клиент
прекращает ожидание; уже запущенный удалённый пайплайн автоматически не отменяется.
В логе сохраняется его ID. При ошибке сбора значение в таблице не меняется.

`GITLAB_MOCK_SCENARIO` передаётся в пайплайн как `MOCK_SCENARIO`.
Сценарии: `release`, `release69`, `hotfix`, `mixed`, `empty`, `failed`, `trace`.
Для рабочего collect-images оставьте эту настройку пустой.

API: [запуск пайплайна](https://docs.gitlab.com/api/pipelines/),
[джобы](https://docs.gitlab.com/api/jobs/),
[артефакты](https://docs.gitlab.com/api/job_artifacts/).

## Серверный запуск и секреты

Сервис принимает настройки из окружения, а Google credentials — из файла.
На сервере доставляйте секреты из Vault средствами вашей платформы: токены в
`JIRA_TOKEN` / `GITLAB_TOKEN`, JSON service account в файл, доступный только процессу
сервиса. В `GOOGLE_CREDENTIALS_JSON` укажите путь смонтированного файла.
Vault-клиент в приложение не встроен; пути и способ авторизации Vault задаёт ваша
инфраструктура. Не сохраняйте секреты в образе контейнера или YAML репозитория.
После доставки настроек запуск: `env-release-tracker -daemon -write`.

## Проверка перед коммитом

`.gitignore` исключает `.env.*`, `env.sh`, `secrets/` и типовые JSON-файлы
ключей/токенов; `.env.example` остаётся в Git. Это не заменяет проверку
содержимого.

Установите [Gitleaks](https://github.com/gitleaks/gitleaks) и выполните перед
коммитом:

```bash
gitleaks git --redact --log-opts="--all" .
gitleaks dir --redact .
```

Проверка не требует токенов Jira, GitLab или Google. Не используйте реальные
ключи в тестах.

Проверка запускается вручную: конвейер CI в репозитории не заведён — способ
запуска сервиса и шаблоны CI остаются открытым вопросом (пункт 11 ТЗ).
