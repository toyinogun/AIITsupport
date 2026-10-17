# Everyday targets. The runbook points at these, not at long commands.
# `make seal` and `make release` arrive with feature #17.

SHELL := /bin/bash
BINARIES := agent toolserver
LOAD_ENV := set -a && . ./.env && set +a
TOOL := go tool -modfile=tools.mod
GIT_HOOKS := pre-commit pre-push

.PHONY: dev db-up db-down db-roles migrate build test vet gen docker clean \
	lint fmt fmt-check gen-check tidy-check vuln secrets hooks check

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

## db-roles: create the app roles in the Compose Postgres (safe to run again)
db-roles: db-up
	docker compose exec -T postgres psql -q -v ON_ERROR_STOP=1 -U noord -d noord < db/roles.sql

## migrate: apply goose migrations as the owner role (MIGRATE_DATABASE_URL from .env)
migrate: .env db-roles
	@$(LOAD_ENV) && go tool goose -dir db/migrations postgres \
		"$${MIGRATE_DATABASE_URL:?is not set; copy the database lines from .env.example into .env}" up

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

## lint: run golangci-lint (linters plus a format check)
lint:
	$(TOOL) golangci-lint run

## fmt: format every Go file with gofumpt and goimports
fmt:
	$(TOOL) golangci-lint fmt

## fmt-check: fail if any Go file needs formatting
fmt-check:
	$(TOOL) golangci-lint fmt --diff

## gen-check: fail if the sqlc code is out of date with db/queries
gen-check: gen
	git diff --exit-code -- internal/db/queries
	@untracked=$$(git ls-files --others --exclude-standard internal/db/queries); \
	if [ -n "$$untracked" ]; then echo "untracked sqlc output:"; echo "$$untracked"; exit 1; fi

## tidy-check: fail if go.mod or tools.mod needs go mod tidy
tidy-check:
	go mod tidy -diff
	go mod tidy -diff -modfile=tools.mod

## vuln: scan the app code for known vulnerabilities
vuln:
	$(TOOL) govulncheck ./...

## secrets: scan the full git history for secrets
secrets:
	$(TOOL) gitleaks git --config .gitleaks.toml --no-banner

## hooks: install git hook shims that run lefthook through go tool
hooks:
	@hooks_dir=$$(git rev-parse --git-path hooks) && mkdir -p "$$hooks_dir" && \
	for h in $(GIT_HOOKS); do \
		printf '#!/bin/sh\ncd "$$(git rev-parse --show-toplevel)" && exec go tool -modfile=tools.mod lefthook run %s "$$@"\n' $$h > "$$hooks_dir/$$h" && \
		chmod +x "$$hooks_dir/$$h" && echo "wrote $$hooks_dir/$$h"; \
	done

## check: everything CI runs except the image builds
check: lint gen-check tidy-check vuln secrets test

## docker: build both container images for the local platform
docker:
	@for b in $(BINARIES); do \
		docker build --build-arg CMD=$$b -t aiitsupport-$$b:dev . || exit 1; \
	done

clean:
	rm -rf bin
