.PHONY: help up down logs ps build run tidy fmt vet test test-integration migrate migrate-all web-install web-dev lint docker-build docker-build-one bootstrap keys deps

SERVICE ?= tender-intel

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-15s %s\n",$$1,$$2}'

up: ## Start local infra (postgres, kafka, temporal, otel, grafana...)
	docker compose up -d

down: ## Stop local infra
	docker compose down

logs: ## Tail infra logs
	docker compose logs -f --tail=100

ps:
	docker compose ps

tidy:
	go mod tidy

fmt:
	gofmt -s -w .

vet:
	go vet ./...

test: ## Run unit tests
	go test ./... -race -count=1

test-integration: ## Run integration tests (requires Docker)
	go test -tags=integration ./internal/tests/... -v -timeout=120s

deps: ## Download and tidy all dependencies (run after cloning)
	go mod download
	go mod tidy

build: ## Build all service binaries to ./bin
	@mkdir -p bin
	go build -o bin/tender-intel        ./cmd/tender-intel
	go build -o bin/tender-intel-worker ./cmd/tender-intel-worker
	go build -o bin/audit               ./cmd/audit
	go build -o bin/document            ./cmd/document
	go build -o bin/submission          ./cmd/submission
	go build -o bin/submission-worker   ./cmd/submission-worker
	go build -o bin/esign               ./cmd/esign
	go build -o bin/console-bff         ./cmd/console-bff
	go build -o bin/identity            ./cmd/identity

web-install: ## Install frontend deps
	cd web/console && npm install

web-dev: ## Run Next.js dev server (web/console)
	cd web/console && npm run dev

lint: ## Run golangci-lint (requires installation)
	golangci-lint run --timeout=5m

docker-build: ## Build docker images for all services
	./scripts/build-images.sh

docker-build-one: ## Build single docker image (SERVICE=audit)
	./scripts/build-images.sh $(SERVICE)

run: ## Run a service locally (SERVICE=tender-intel | tender-intel-worker)
	go run ./cmd/$(SERVICE)

migrate: ## Apply SQL migrations for a service (SERVICE=tender-intel)
	@for f in migrations/$(SERVICE)/*.sql; do \
		echo ">> $$f"; \
		docker compose exec -T postgres psql -U platform -d platform -f - < $$f; \
	done

migrate-all: ## Apply all migrations for all services
	@for svc in migrations/*/; do \
		svc=$$(basename $$svc); \
		echo "=== $$svc ==="; \
		$(MAKE) migrate SERVICE=$$svc; \
	done

keys: ## Generate random dev keys and add to .env (run once)
	@echo "Generating DEV keys..."
	@ESIGN_KEY=$$(openssl rand -hex 32); \
	TOTP_KEY=$$(openssl rand -hex 32); \
	sed -i.bak \
		-e "s/^ESIGN_MASTER_KEY_HEX=.*/ESIGN_MASTER_KEY_HEX=$$ESIGN_KEY/" \
		-e "s/^IDENTITY_TOTP_MASTER_KEY_HEX=.*/IDENTITY_TOTP_MASTER_KEY_HEX=$$TOTP_KEY/" \
		.env && rm -f .env.bak; \
	echo "ESIGN_MASTER_KEY_HEX=$$ESIGN_KEY"; \
	echo "IDENTITY_TOTP_MASTER_KEY_HEX=$$TOTP_KEY"

bootstrap: ## Create first DEV user (requires identity service running with DEV_SKIP_MFA=1)
	./scripts/bootstrap-dev.sh
