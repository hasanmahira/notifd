.PHONY: help up down build logs ps test lint tidy migrate-up migrate-down swagger seed load clean

SHELL := /bin/bash

help:
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

up: ## Start the full stack (postgres, redis, mockserver, migrate, api, worker, traefik)
	docker compose up -d --build

obs-up: ## Start the stack + Prometheus + Loki + Promtail + Grafana
	docker compose -f docker-compose.yml -f docker-compose.observability.yml up -d --build
	@echo ""
	@echo "Grafana      http://localhost:3000         (admin / admin)"
	@echo "Prometheus   http://localhost:9090"
	@echo "Loki         http://localhost:3100/ready"
	@echo "App          http://localhost:8080"
	@echo "Traefik      http://localhost:8081/dashboard/"

obs-down: ## Stop the observability overlay (leaves app stack running)
	docker compose -f docker-compose.observability.yml down

scale: ## Bring up the stack with api=2, worker=3 behind the Traefik reverse proxy
	docker compose up -d --build --scale api=2 --scale worker=3
	@echo ""
	@echo "2 api replicas behind Traefik at :8080 (dashboard on :8081)"
	@echo "3 worker replicas share one Redis token bucket"
	@echo ""
	@echo "Try it:"
	@echo "  curl -sS http://localhost:8080/healthz  (round-robin across api pods)"
	@echo "  open http://localhost:8081/dashboard/   (Traefik routing view)"
	@echo "  ./scripts/load_test.sh 500 sms          (100/s cap holds across workers)"

down: ## Stop the stack and remove containers
	docker compose down -v

build: ## Build api and worker binaries locally
	CGO_ENABLED=0 go build -trimpath -o bin/api    ./cmd/api
	CGO_ENABLED=0 go build -trimpath -o bin/worker ./cmd/worker

logs: ## Tail api + worker logs
	docker compose logs -f api worker

ps: ## Show running services
	docker compose ps

test: ## Run all tests with the race detector (portable; no -cover)
	go test -race -count=1 ./...

test-cover: ## Run tests with coverage report (requires Go toolchain w/ covdata)
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint: ## Run golangci-lint (install if missing)
	@if ! command -v golangci-lint >/dev/null; then \
		echo "installing golangci-lint..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	fi
	golangci-lint run ./...

tidy: ## Tidy go modules
	go mod tidy

migrate-up: ## Apply pending migrations against running postgres
	docker compose run --rm migrate

migrate-down: ## Roll back one migration
	docker compose run --rm migrate \
		-path=/migrations \
		-database=postgres://notifd:notifd@postgres:5432/notifd?sslmode=disable \
		down 1

swagger: ## Regenerate OpenAPI spec from handler annotations
	@if ! command -v swag >/dev/null; then \
		go install github.com/swaggo/swag/cmd/swag@latest; \
	fi
	swag init -g cmd/api/main.go -o docs --parseDependency --parseInternal

seed: ## Generate a demo JWT and send a few curl notifications
	./scripts/seed.sh

demo: seed ## Alias for seed

load: ## Smoke-test load generator
	./scripts/load_test.sh 500 sms

clean: ## Remove local binaries and coverage artifacts
	rm -rf bin/ coverage.out coverage.html
