# Сборка — со всем тулингом Go; в финальный образ слой не попадает.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Отдельно от исходников: слой с зависимостями не пересобирается,
# пока не меняются go.mod/go.sum.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/env-release-tracker .
# Точки монтирования томов. В distroless нет шелла, mkdir там не выполнить,
# поэтому каталоги готовятся здесь и копируются с владельцем nonroot.
RUN mkdir -p /out/state /out/google-token

# Рантайм — distroless: ни шелла, ни пакетного менеджера, нечем
# закрепиться при компрометации. Бинарнику для работы ничего из ОС не нужно.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/env-release-tracker ./env-release-tracker
# Правила по средам едут в образе: без них не сработают пропуски
# выключенных и офисных сред. Заменяется монтированием файла
# и переменной OVERRIDES_FILE.
COPY overrides.yaml ./overrides.yaml
# Новый именованный том наследует владельца каталога из образа. Без этого
# Docker создаст его от root, и сервис не сможет сохранить STATE_FILE и токен.
COPY --from=build --chown=nonroot:nonroot /out/state ./state
COPY --from=build --chown=nonroot:nonroot /out/google-token ./google-token

# distroless:nonroot уже запускает процесс под непривилегированным UID —
# отдельный USER не нужен.
ENTRYPOINT ["./env-release-tracker"]
