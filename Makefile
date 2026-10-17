# Everyday targets. The runbook points at these, not at long commands.
# `make seal` and `make release` arrive with feature #17.

SHELL := /bin/bash
BINARIES := agent toolserver
LOAD_ENV := set -a && . ./.env && set +a

.PHONY: dev db-up db-down migrate build test vet gen docker clean

## dev: start Postgres, run migrations, then run both binaries (Ctrl-C stops all)
dev: .env migrate build
	@$(LOAD_ENV) && { \
		./bin/toolserver & ts=$$!; \
		trap 'kill $$ts 2>/dev/null; wait $$ts' EXIT; \
		./bin/agent; \
	}

.env:
	cp .env.example .env
	@echo "Created .env from .env.example. Put real secrets in .env only."

## db-up: start Postgres in Compose and wait until it is healthy
db-up:
	docker compose up -d --wait postgres

## db-down: stop Compose (add -v yourself to also drop the data volume)
db-down:
	docker compose down

## migrate: apply goose migrations to DATABASE_URL from .env
migrate: .env db-up
	@$(LOAD_ENV) && go tool goose -dir db/migrations postgres "$$DATABASE_URL" up

## build: compile both binaries into bin/
build:
	@for b in $(BINARIES); do go build -o bin/$$b ./cmd/$$b || exit 1; done

## test: run every test with the race detector
test:
	go test -race ./...

## vet: run go vet
vet:
	go vet ./...

## gen: regenerate sqlc code from db/queries
gen:
	go tool sqlc generate

## docker: build both container images for the local platform
docker:
	@for b in $(BINARIES); do \
		docker build --build-arg CMD=$$b -t aiitsupport-$$b:dev . || exit 1; \
	done

clean:
	rm -rf bin
