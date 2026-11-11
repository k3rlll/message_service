-include .env

SHELL := bash.exe

run:
	go run ./cmd/app
# Выпустить dev-токен (заглушка сервиса авторизации): make token USER=<ULID>
token:
	go run ./cmd/devtoken $(if $(USER),-user $(USER))
test:
	go test ./...
env-build:
	docker compose build
env-up:
	docker compose up -d
env-down:
	docker compose down
env-cleanup:
	read -p "Are you sure you want to delete all data? [y/n]:" ans; \
	if [ "$$ans" = "y" ] || [ "$$ans" = "Y" ]; then \
		docker compose down -v; \
	fi
