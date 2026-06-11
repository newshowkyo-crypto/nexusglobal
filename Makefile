.PHONY: up down migrate sqlc build test test-short lint run-api run-worker ci redlines

up:            ## Start PG + Redis
	docker compose up -d

down:          ## Stop and remove containers
	docker compose down -v

migrate:       ## Run database migrations
	go run ./cmd/migrate up

sqlc:          ## Generate sqlc code
	sqlc generate

build:         ## Compile all packages
	go build ./...

test:          ## Full tests (integration, needs up)
	go test ./... -count=1 -race

test-short:    ## Unit tests only (skip testcontainers)
	go test ./... -short -count=1

lint:          ## Static analysis
	gofmt -l .
	go vet ./...

run-api:       ## Run API server
	go run ./cmd/api

run-worker:    ## Run worker
	go run ./cmd/worker

ci:            ## Full CI check: build + lint + test-short
	$(MAKE) build && $(MAKE) lint && $(MAKE) test-short

redlines:      ## CONSTRAINTS.md redline checks
	@echo "=== NF-03: Check float in financial paths ==="
	@rg -n "float(32|64)" internal/ledger internal/payout internal/mae 2>/dev/null && echo "VIOLATION NF-03" || echo "OK: No float in financial paths"
	@echo "=== RED-01/02: Check external calls in handler/service ==="
	@rg -n "lpClient\.|bankClient\.|maeClient\." internal/*/handler internal/*/service 2>/dev/null && echo "CHECK RED-01/02" || echo "OK: No external calls in handler/service"
	@echo "=== RED-06: Check exposure decrement ==="
	@rg -n "DecrBy" internal/mae 2>/dev/null && echo "CHECK RED-06" || echo "OK: No exposure decrement"
	@echo "=== RED-08: Check Cooldown->Active ==="
	@rg -n "Cooldown.*Active" internal/mae/domain 2>/dev/null && echo "CHECK RED-08" || echo "OK: No direct Cooldown->Active"
	@echo "=== Done ==="
