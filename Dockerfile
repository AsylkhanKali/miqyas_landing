# Универсальный multi-stage Dockerfile для всех Go-сервисов платформы.
# Имя сервиса передаётся через --build-arg SERVICE=<name>, где <name>
# совпадает с поддиректорией в ./cmd (tender-intel, audit, document, ...).
#
# Образ:
#   - distroless/static:nonroot — минимум поверхностей атаки;
#   - запускается под UID 65532 (nonroot), без shell, без apk/apt;
#   - бинарь полностью статичен (CGO_ENABLED=0).

# ─── builder ───────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

ARG SERVICE
RUN test -n "$SERVICE" || (echo "ERROR: --build-arg SERVICE=<name> is required" && false)

WORKDIR /src

# Кешируем go.mod / go.sum отдельно от исходников.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0 GOOS=linux GOFLAGS=-trimpath
RUN go build \
    -ldflags="-s -w -X main.version=${SERVICE}" \
    -o /out/app ./cmd/${SERVICE}

# ─── runtime ───────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/app /app

USER nonroot:nonroot
ENTRYPOINT ["/app"]
