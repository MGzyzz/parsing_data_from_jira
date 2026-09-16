# Сборка — со всем тулингом Go; в финальный образ слой не попадает.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Отдельно от исходников: слой с зависимостями не пересобирается,
# пока не меняются go.mod/go.sum.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/env-release-tracker .

# Рантайм — distroless: ни шелла, ни пакетного менеджера, нечем
# закрепиться при компрометации. Бинарнику для работы ничего из ОС не нужно.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/env-release-tracker ./env-release-tracker
COPY overrides.example.yaml ./overrides.example.yaml

# distroless:nonroot уже запускает процесс под непривилегированным UID —
# отдельный USER не нужен.
ENTRYPOINT ["./env-release-tracker"]
