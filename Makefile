-include .env

ifeq ($(OS),Windows_NT)
SHELL := bash.exe
endif

GOLANGCI_LINT_VERSION := v2.15.0

.PHONY: run token test test-integration lint env-build env-up env-up-monitoring env-down env-cleanup

## Запуск сервиса локально (Mongo, Redis и S3 должны быть доступны)
run:
	go run ./cmd/app

## Dev-токен (заглушка сервиса авторизации): make token USER_ID=<ULID>
token:
	go run ./cmd/devtoken $(if $(USER_ID),-user $(USER_ID))

## Unit- и e2e-тесты с детектором гонок
test:
	go test -race -count=1 ./...

## Интеграционные тесты против настоящих MongoDB и Redis (нужен Docker)
test-integration:
	go test -tags integration -race -count=1 ./internal/database/... ./internal/broker/...

lint:
	docker run --rm -v "$(CURDIR):/src" -w /src golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run ./...

env-build:
	docker compose build
## Сервис (2 экземпляра за nginx), MongoDB, Redis, S3
env-up:
	docker compose up -d
## То же плюс Prometheus и Grafana
env-up-monitoring:
	docker compose --profile monitoring up -d
env-down:
	docker compose --profile monitoring down
env-cleanup:
	read -p "Are you sure you want to delete all data? [y/n]:" ans; \
	if [ "$$ans" = "y" ] || [ "$$ans" = "Y" ]; then \
		docker compose --profile monitoring down -v; \
	fi
