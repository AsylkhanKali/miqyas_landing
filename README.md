# goszakup-platform

Корпоративная платформа операций электронных закупок (РК).
**Только штатные пользовательские сценарии**, без обхода защит площадок, с human-in-the-loop на критических переходах. Подробности — в архитектурном обзоре (см. внутренние материалы).

## Сейчас в репозитории

- Монорепо на Go 1.23 (`github.com/goszakup/platform`).
- Локальная инфра в `docker-compose.yml`: Postgres, Redis, Kafka, Temporal (+UI), OTel Collector, Prometheus, Grafana, Tempo, Loki, Vault (dev).
- Сервис **`tender-intel`** — read-only, обращается только к публичным открытым API `goszakup.gov.kz`.
- Сервис **`tender-intel-worker`** — Temporal worker с durable `TenderSyncWorkflow` (cron, идемпотентный upsert).
- Сервис **`audit`** — append-only журнал с цепочкой хэшей (SHA-256), запрет UPDATE/DELETE на уровне БД.
- Сервис **`document`** — шаблоны и документы с валидацией (JSON Schema + доменные правила), хранение в S3/MinIO, иммутабельные версии.
- Сервис **`submission`** (API) + **`submission-worker`** (Temporal) — durable оркестрация подачи с FSM `draft → reviewed → signed → submitted → acknowledged → archived`, human-in-the-loop через signals, защита окна T-30 минут перед дедлайном.
- Сервис **`esign`** — брокер ЭЦП-операций. Software-backend для DEV (AES-GCM шифрование приватных ключей), PKCS#11 — каркас для prod-HSM. Обязательный аудит каждой операции подписи.
- Сервис **`console-bff`** + **`web/console`** (Next.js 14) — рабочее место оператора. BFF агрегирует данные из всех бэкендов и вычисляет UI-подсказки (allowed actions, окно cutoff). Фронт — App Router + TS, минимальный.
- Сервис **`identity`** — пользователи, argon2id-пароли, обязательный TOTP (RFC 6238), JWT RS256, JWKS. Общая middleware `internal/platform/auth` валидирует токены через JWKS-кеш для остальных сервисов.
- Общие библиотеки: `internal/platform/{logger,otelx,httpx,pgxdb,auditclient}`.

## Требования

- Go 1.23+ (`brew install go`)
- Docker Desktop / OrbStack

## Быстрый старт

```sh
cp .env.example .env
make deps               # go mod download + tidy (один раз)
make keys               # сгенерировать ESIGN_MASTER_KEY_HEX и IDENTITY_TOTP_MASTER_KEY_HEX в .env
make up                 # поднять локальную инфру (Postgres, Redis, Kafka, Temporal…)
make migrate-all        # применить все SQL-миграции
set -a; source .env; set +a

# Запустить identity первым (остальные сервисы валидируют JWT через него)
make run SERVICE=identity

# В отдельном терминале: создать первого пользователя
make bootstrap          # регистрация admin@dev.local, TOTP-энроллмент
```

После bootstrap — стартовать остальные сервисы:
```sh
make run SERVICE=audit
make run SERVICE=document
make run SERVICE=submission
make run SERVICE=submission-worker
make run SERVICE=esign
make run SERVICE=tender-intel
make run SERVICE=tender-intel-worker
make run SERVICE=console-bff
make web-dev            # Next.js UI на :3000
```

Проверки `tender-intel`:
```sh
curl localhost:8081/healthz
curl localhost:8081/readyz
curl localhost:8081/api/v1/upstream/health
```

Проверки `audit`:
```sh
make run SERVICE=audit                    # в отдельном терминале
curl localhost:8082/healthz
# отправить событие
curl -X POST localhost:8082/api/v1/events \
  -H "Authorization: Bearer $AUDIT_INGEST_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"actor_type":"service","actor_id":"tender-intel-worker","action":"tender.synced","resource":"tender:12345","metadata":{"page":0}}'
# выборка
curl 'localhost:8082/api/v1/events?action=tender.synced&limit=10'
# проверка целостности цепочки
curl localhost:8082/api/v1/verify
```

Проверки `document`:
```sh
make run SERVICE=document     # в отдельном терминале
# завести шаблон
curl -X PUT localhost:8083/api/v1/templates \
  -H 'Content-Type: application/json' \
  -d '{
    "code":"price-offer",
    "name":"Ценовое предложение",
    "schema":{
      "type":"object",
      "required":["amount","currency","bin"],
      "properties":{
        "amount":{"type":"number","minimum":0},
        "currency":{"type":"string","enum":["KZT","USD"]},
        "bin":{"type":"string"}
      }
    },
    "rules":[
      {"kind":"min_amount","params":{"field":"amount","min":1000}},
      {"kind":"bin","params":{"field":"bin"}}
    ],
    "actor":"admin@example.kz"
  }'
# создать документ
curl -X POST localhost:8083/api/v1/documents \
  -H 'Content-Type: application/json' \
  -d '{
    "org_id":"123456789012",
    "template_code":"price-offer",
    "title":"Оферта по тендеру X",
    "created_by":"operator@example.kz",
    "payload":{"amount":250000,"currency":"KZT","bin":"123456789012"}
  }'
```

Проверки `submission`:
```sh
make run SERVICE=submission         # терминал 1 (API на :8084)
make run SERVICE=submission-worker  # терминал 2

# 1) Стартуем подачу (нужны реальные uuid документа из document-сервиса)
curl -X POST localhost:8084/api/v1/submissions \
  -H 'Content-Type: application/json' \
  -d '{
    "org_id":"123456789012",
    "tender_id":"T-2026-001",
    "platform":"goszakup",
    "document_id":"<uuid из /api/v1/documents>",
    "document_version":1,
    "deadline_at":"2026-06-01T12:00:00Z",
    "created_by":"operator@example.kz"
  }'
# → { id: "<sid>", state: "draft", ... }

# 2) Оператор согласовал
curl -X POST localhost:8084/api/v1/submissions/<sid>/review \
  -H 'Content-Type: application/json' -d '{"actor":"reviewer@example.kz"}'

# 3) ЭЦП применена (на стороне Operator Console / HSM Broker)
curl -X POST localhost:8084/api/v1/submissions/<sid>/sign \
  -H 'Content-Type: application/json' \
  -d '{"actor":"operator@example.kz","esig_cert_cn":"CN=Operator","esig_cert_sha":"deadbeef"}'

# 4) Финальная подача (вне окна T-30 минут — иначе нужен acknowledge_cutoff:true)
curl -X POST localhost:8084/api/v1/submissions/<sid>/submit \
  -H 'Content-Type: application/json' \
  -d '{"actor":"operator@example.kz","idempotency_key":"<sid>-v1"}'

# 5) Состояние и журнал переходов
curl localhost:8084/api/v1/submissions/<sid>
```

Проверки `esign`:
```sh
# 32-байтный мастер-ключ для DEV
export ESIGN_MASTER_KEY_HEX=$(openssl rand -hex 32)
make run SERVICE=esign

# 1) Зарегистрировать ключ (DEV: генерируется самоподписанный сертификат)
curl -X POST localhost:8085/api/v1/keys \
  -H 'Content-Type: application/json' \
  -d '{"org_id":"123456789012","owner":"operator@example.kz","subject_cn":"Operator KZ","key_size":2048}'
# → { id: "<key-id>", cert_pem: "...", backend: "software", ... }

# 2) Подписать произвольные байты
PAYLOAD=$(printf 'hello esign' | base64)
curl -X POST localhost:8085/api/v1/sign \
  -H 'Content-Type: application/json' \
  -d "{\"key_id\":\"<key-id>\",\"actor\":\"operator@example.kz\",\"purpose\":\"document:abc:v1\",\"data_base64\":\"$PAYLOAD\"}"
# → { signature_base64: "..." }
```

Проверки `identity`:
```sh
export IDENTITY_TOTP_MASTER_KEY_HEX=$(openssl rand -hex 32)
make run SERVICE=identity

# 1) Зарегистрировать
curl -X POST localhost:8086/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.kz","full_name":"Alice","org_id":"123456789012","password":"verysecretpass123","roles":["operator"]}'

# 2) JWKS (публичный)
curl localhost:8086/.well-known/jwks.json

# 3) DEV: IDENTITY_DEV_SKIP_MFA=true в .env позволяет залогиниться без TOTP
#    для первичного enrollment. После make bootstrap — убрать этот флаг.
# 4) Полный login с TOTP
curl -X POST localhost:8086/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@dev.local","password":"admin123456789","totp_code":"<code из Authenticator>"}'
# → { access_token: "...", refresh_token: "...", expires_at: "..." }
```

**Operator Console (BFF + Web UI):**
```sh
make run SERVICE=console-bff   # терминал 1, BFF :8090
make web-install               # один раз
make web-dev                   # терминал 2, Next.js :3000 → http://localhost:3000
```
Открыть подачу: главная страница → ввести UUID submission → перейти на `/submissions/<id>`.
Доступные кнопки (review/sign/submit/cancel) рассчитываются BFF на основе состояния
и окна T-30 минут — фронт не дублирует бизнес-правила.

UI:
- Operator Console — http://localhost:3000
- Grafana — http://localhost:3001 (перенесена с 3000)

**Аутентификация API:**
Все эндпоинты (кроме `/healthz`, `/readyz`, `/metrics`, `/.well-known/*`) требуют:
```sh
Authorization: Bearer <access_token>
```
Токен получается через `POST /api/v1/auth/login` в identity-сервисе (`:8086`).

**Метрики:**
Каждый сервис экспортирует базовые HTTP-метрики на `/metrics`:
```sh
curl localhost:8081/metrics   # tender-intel
curl localhost:8082/metrics   # audit
curl localhost:8083/metrics   # document
# ... и т.д.
```

## Тесты

```sh
make test                  # unit-тесты (без Docker, без сети)
make test-integration      # интеграционные тесты (требует Docker)
```

**Unit-тесты** (без внешних зависимостей):
- `internal/audit/storage` — детерминизм и зависимость от `prev_hash` для `computeHash`.
- `internal/document/validator` — JSON Schema + доменные правила (`min_amount`, `bin`, `deadline_before`, unknown).
- `internal/submission/workflows` — Temporal `testsuite`: happy path, отклонение submit в окне cutoff без acknowledge, проход с acknowledge, cancel из reviewed, истёкший дедлайн.
- `internal/console/aggregator` — таблица `allowedActionsFor` и `computeHints` (вкл./выкл. cutoff, дедлайн в прошлом).

**Интеграционные тесты** (`//go:build integration`, требуют Docker):
- `internal/tests` — Audit: Append+VerifyChain, List-by-action, TamperDetection.
- Document storage: CREATE + версии + иммутабельность (UPDATE → 0 rows affected).
- Validator: полная таблица валидации через реальный `domain.Template`.
- UUID + hex-key roundtrip тесты.

Интеграционные тесты с реальным Postgres (audit `VerifyChain`, document `AddVersion` и т.д.) появятся отдельным шагом под build tag `integration` через testcontainers.

## CI / Docker

- `.github/workflows/go-ci.yml` — lint (golangci-lint), `go vet`, `go test -race` с артефактом покрытия.
- `.github/workflows/web-ci.yml` — typecheck (`tsc --noEmit`) + `next build`.
- `.github/workflows/docker-build.yml` — матрица сборки 8 сервисов через единый `Dockerfile` с `--build-arg SERVICE=<name>`, публикация в GHCR на `push`, Trivy-скан на PR.
- `.golangci.yml` — конфиг линтера (errcheck, staticcheck, gosimple, bodyclose, errorlint, revive).
- Образы: `distroless/static:nonroot`, статичный бинарь, `USER nonroot`.

Локально:
```sh
./scripts/build-images.sh                  # все
./scripts/build-images.sh audit            # один
TAG=v0.1 ./scripts/build-images.sh         # с тегом
make lint                                  # требует установленного golangci-lint
```
- Temporal UI — http://localhost:8088
- Prometheus — http://localhost:9090
- MinIO Console — http://localhost:9001 (логин `platform` / `platform-secret`)

## Что НЕ делает (намеренно)

- не подаёт заявки, не подписывает ЭЦП, не управляет сессиями операторов;
- не делает браузерную автоматизацию против площадок;
- не обходит CAPTCHA, антибот-механизмы или rate limits.

Эти возможности появятся в следующих фазах с обязательным human-in-the-loop и аудит-журналом.

## Структура

```
cmd/<service>/                — main-пакеты сервисов
internal/platform/            — общие библиотеки (config, logger, otel, http, pg)
internal/<service>/           — внутренние пакеты сервиса
migrations/<service>/         — SQL миграции
deploy/                       — конфиги локальной инфры
```

## Дорожная карта

См. архитектурный обзор. Сейчас закрыта Фаза 0 + начало Фазы 1 (Tender Intelligence read-only).
