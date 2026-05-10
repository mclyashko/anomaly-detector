.PHONY: help test test-unit test-integration build-up build-down build-clean lint vet tidy fmt clean run-ml python-setup

COMPOSE := docker compose -f infra/docker-compose.yml

help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:[^#]*##' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = " ## "}; {printf "  %-22s %s\n", $$1, $$2}'

test: test-unit test-integration ## Run unit and integration tests

test-unit: ## Run unit tests for all services
	@for svc in agent fake-service ingestion analyzer notifier; do \
		echo "=== $$svc ===" && \
		(cd services/$$svc && go test ./... -race) || exit 1; \
	done
	@echo "=== training (Python) ===" && \
	.venv/bin/python -m pytest services/training/tests/ -v || exit 1

test-integration: ## Run integration tests
	go test ./tests/integration/... -v

build-up: ## Build and start all services (including training service)
	$(COMPOSE) up --build

build-down: ## Stop all services
	$(COMPOSE) down

build-clean: ## Stop and remove volumes
	$(COMPOSE) down -v

lint: ## Run go vet on all services
	@for svc in agent fake-service ingestion analyzer notifier; do \
		echo "=== $$svc ===" && \
		(cd services/$$svc && go vet ./... 2>&1) || exit 1; \
	done

tidy: ## Run go mod tidy for all modules
	@for dir in shared/ services/agent/ services/fake-service/ services/ingestion/ services/analyzer/ services/notifier/; do \
		(cd $$dir && go mod tidy && echo "$$dir: ok") || echo "$$dir: FAILED"; \
	done

fmt: ## Run gofmt -l (list unformatted files)
	@found=$$(find . -name '*.go' -not -path './.git/*' -not -path './tests/integration/*' | xargs gofmt -l 2>/dev/null); \
	if [ -n "$$found" ]; then echo "$$found"; exit 1; fi

clean: ## Remove build artifacts
	@find . -name '*_test' -type f -delete 2>/dev/null; true
	@for dir in shared/ services/agent/ services/fake-service/ services/ingestion/ services/analyzer/ services/notifier/; do \
		(cd $$dir && go clean ./... 2>&1) || true; \
	done

python-setup: ## Create venv and install dependencies
	python3 -m venv .venv
	.venv/bin/pip install -r services/training/requirements.txt

run-ml: python-setup ## Run training service locally (port 8085)
	PYTHONPATH=services/training .venv/bin/python -m uvicorn services.training.app:app --host 0.0.0.0 --port 8085
