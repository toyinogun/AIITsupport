# Everyday targets. The runbook points at these, not at long commands.
# `make seal` and `make release` arrive with the rest of feature #17.

SHELL := /bin/bash
BINARIES := agent toolserver
LOAD_ENV := set -a && . ./.env && set +a
TOOL := go tool -modfile=tools.mod
GIT_HOOKS := pre-commit pre-push
# Zammad's services and named volumes (spec 0006). helpdesk-reset removes
# exactly these, never pgdata and never Samba.
ZAMMAD_SERVICES := zammad-init zammad-elasticsearch zammad-memcached zammad-postgresql \
	zammad-redis zammad-railsserver zammad-scheduler zammad-websocket zammad-nginx
ZAMMAD_VOLUMES := noord_elasticsearch-data noord_postgresql-data noord_redis-data \
	noord_zammad-backup noord_zammad-storage

.PHONY: dev db-up db-down db-roles migrate build test vet gen docker clean \
	lint fmt fmt-check gen-check tidy-check vuln secrets hooks check \
	certs directory-up seed directory-reset directory-login directory-lock \
	entra-sync entra-mfa noord-reset helpdesk-up helpdesk-seed helpdesk-reset install smoke \
	release pause resume uninstall seal-key-backup cluster-init deploy-check

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

## certs: make the dev CA and the Samba server cert in .dev/certs (keeps existing files)
certs:
	go run ./cmd/seed certs

## directory-up: start Samba AD in Compose and wait until LDAPS answers
directory-up: .env certs
	@$(LOAD_ENV) && docker compose up -d --build --wait samba

## seed: fill the directory from seed/directory/noord.yaml (safe to rerun)
seed: .env
	@$(LOAD_ENV) && go run ./cmd/seed directory

## directory-reset: replace the Samba container with a fresh one and seed it (Postgres untouched)
directory-reset: .env certs
	docker compose rm -sf samba
	@$(MAKE) --no-print-directory directory-up seed

## directory-login: bind as a seeded user and show the lock state (NAME=<username>)
directory-login: .env
	@test -n "$(NAME)" || { echo "usage: make directory-login NAME=<username>" >&2; exit 1; }
	@$(LOAD_ENV) && go run ./cmd/seed login "$(NAME)"

## directory-lock: lock a seeded user by failed binds (NAME=<username>)
directory-lock: .env
	@test -n "$(NAME)" || { echo "usage: make directory-lock NAME=<username>" >&2; exit 1; }
	@$(LOAD_ENV) && go run ./cmd/seed lock "$(NAME)"

## entra-sync: make the Entra tenant match SYNC-Entra-Users (skips when SEED_GRAPH_* is unset; [ONLY=<username>])
entra-sync: .env
	@$(LOAD_ENV) && go run ./cmd/seed entra sync $(if $(ONLY),--only "$(ONLY)")

## entra-mfa: list a user's MFA methods with the tool server app (NAME=<username> [CLEAR=1])
entra-mfa: .env
	@test -n "$(NAME)" || { echo "usage: make entra-mfa NAME=<username> [CLEAR=1]" >&2; exit 1; }
	@$(LOAD_ENV) && go run ./cmd/seed entra mfa $(if $(CLEAR),--clear) "$(NAME)"

## noord-reset: a fresh Samba directory, an Entra sync, then a fresh helpdesk (Postgres untouched; not atomic)
noord-reset:
	@$(MAKE) --no-print-directory directory-reset entra-sync helpdesk-reset

## helpdesk-up: start Zammad in Compose (profile helpdesk) and wait until it is healthy
# and zammad-init has finished: it configures Elasticsearch last, and a seed that
# starts before that leaves its LDAP users out of the search index (journal 0028).
helpdesk-up: .env certs
	@$(LOAD_ENV) && docker compose --profile helpdesk up -d --wait $(ZAMMAD_SERVICES) && \
		docker compose --profile helpdesk wait zammad-init

## helpdesk-seed: fill Zammad from seed/helpdesk/ (says "already seeded", or asks for a reset)
helpdesk-seed: .env
	@$(LOAD_ENV) && go run ./cmd/seed helpdesk

## helpdesk-reset: a fresh Zammad, seeded from seed/helpdesk/ (Samba and pgdata untouched)
helpdesk-reset: .env certs
	@$(MAKE) --no-print-directory directory-up seed
	docker compose --profile helpdesk rm -sfv $(ZAMMAD_SERVICES)
	docker volume rm -f $(ZAMMAD_VOLUMES)
	@$(MAKE) --no-print-directory helpdesk-up helpdesk-seed

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

## deploy-check: lint and render the charts for every cluster folder, validate against the schemas (needs helm)
deploy-check:
	./deploy/check.sh

## check: everything CI runs except the image builds
check: lint gen-check tidy-check vuln secrets test

## docker: build the agent, tool server, and seed images for the local platform
docker:
	@for b in $(BINARIES); do \
		docker build --target app --build-arg CMD=$$b -t aiitsupport-$$b:dev . || exit 1; \
	done
	docker build --target seed --build-arg CMD=seed -t aiitsupport-seed:dev .

## install: install or update a cluster through ArgoCD, CLUSTER=<name> (spec 0015; exit 2 means nothing changed)
install:
	@test -n "$(CLUSTER)" || { echo "usage: make install CLUSTER=<name>" >&2; exit 1; }
	@go build -o bin/seed ./cmd/seed  # built, not go run, so exit 2 reaches you
	./bin/seed install --cluster "$(CLUSTER)"

## smoke: run the smoke check on an installed cluster, CLUSTER=<name> (spec 0015, AC-17)
smoke:
	@test -n "$(CLUSTER)" || { echo "usage: make smoke CLUSTER=<name>" >&2; exit 1; }
	@go build -o bin/seed ./cmd/seed
	./bin/seed smoke --cluster "$(CLUSTER)"

## cluster-init: make deploy/clusters/<name>/ from the template and the live cluster, CLUSTER=<name> (spec 0015)
cluster-init:
	@test -n "$(CLUSTER)" || { echo "usage: make cluster-init CLUSTER=<name>" >&2; exit 1; }
	@go build -o bin/seed ./cmd/seed
	./bin/seed cluster-init --cluster "$(CLUSTER)"

## release: roll a cluster to the images of a commit, CLUSTER=<name> SHA=<sha> (spec 0015, AC-19)
release:
	@test -n "$(CLUSTER)" -a -n "$(SHA)" || { echo "usage: make release CLUSTER=<name> SHA=<sha>" >&2; exit 1; }
	@go build -o bin/seed ./cmd/seed
	./bin/seed release --cluster "$(CLUSTER)" --sha "$(SHA)"

## pause: stop the agent and the seed Jobs, CLUSTER=<name> (AC-20)
pause:
	@test -n "$(CLUSTER)" || { echo "usage: make pause CLUSTER=<name>" >&2; exit 1; }
	@go build -o bin/seed ./cmd/seed
	./bin/seed pause --cluster "$(CLUSTER)"

## resume: start the agent again and rerun every seed step, CLUSTER=<name> (AC-20)
resume:
	@test -n "$(CLUSTER)" || { echo "usage: make resume CLUSTER=<name>" >&2; exit 1; }
	@go build -o bin/seed ./cmd/seed
	./bin/seed resume --cluster "$(CLUSTER)"

## uninstall: delete a cluster's Applications and volumes, CLUSTER=<name> (AC-21)
uninstall:
	@test -n "$(CLUSTER)" || { echo "usage: make uninstall CLUSTER=<name>" >&2; exit 1; }
	@go build -o bin/seed ./cmd/seed
	./bin/seed uninstall --cluster "$(CLUSTER)"

## seal-key-backup: save the Sealed Secrets keys to ~/.noord/<name>/, CLUSTER=<name> (AC-22)
seal-key-backup:
	@test -n "$(CLUSTER)" || { echo "usage: make seal-key-backup CLUSTER=<name>" >&2; exit 1; }
	@go build -o bin/seed ./cmd/seed
	./bin/seed seal-key-backup --cluster "$(CLUSTER)"

clean:
	rm -rf bin
