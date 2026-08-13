.PHONY: install lint test test-unit test-e2e run seed down

install:
	uv sync --dev
	cd services/gateway && go mod download

lint:
	uv run ruff check services/agent services/mcp-tools tests scripts
	uv run ruff format --check services/agent services/mcp-tools tests scripts
	cd services/gateway && test -z "$$(gofmt -l .)" && go vet ./...

test: test-unit

test-unit:
	uv run pytest -m "not e2e" --cov --cov-report=term-missing
	cd services/gateway && go test ./...

test-e2e:
	docker compose -f compose.yaml -f compose.e2e.yaml up --build -d --wait
	RUN_E2E=1 uv run pytest -m e2e tests/e2e

run:
	docker compose up --build

seed:
	uv run python scripts/seed_knowledge.py

down:
	docker compose down
