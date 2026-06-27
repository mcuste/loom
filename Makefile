PKG := ./cmd/loom
BIN := loom

.DEFAULT_GOAL := help

SKILL_SRC := .claude/skills/loom
SKILL_DST := /Users/mcuste/scripts/stow/.claude/skills/loom
WF_SRC := .loom/workflows
WF_DST := $(HOME)/.loom/workflows

.PHONY: help install build clean test test-race fmt vet tidy lint lint-test check run check-all sync-skill sync-workflows

help: ## list available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

install: ## install the loom binary
	go install $(PKG)

build: ## build the loom binary
	go build -o $(BIN) $(PKG)

clean: ## remove the built binary
	rm -f $(BIN)

test: ## run all tests
	go test ./...

test-race: ## run all tests with the race detector
	go test -race ./...

fmt: ## format all Go code
	go fmt ./...

vet: ## run go vet
	go vet ./...

tidy: ## tidy go.mod / go.sum
	go mod tidy

lint: fmt vet ## run all lint checks (fmt + vet)

lint-test: lint test ## run all lint checks then tests

check: ## check one workflow: make check WORKFLOW=path/to.yaml
	go run $(PKG) check $(WORKFLOW)

run: ## run one workflow: make run WORKFLOW=path/to.yaml
	go run $(PKG) run $(WORKFLOW)

check-all: ## check every workflow YAML under workflows/
	@for f in workflows/*.yaml; do \
		echo "=== $$f ==="; \
		go run $(PKG) check "$$f"; \
	done

sync-skill: ## sync the loom skill to the global stow .claude skills dir
	rm -rf $(SKILL_DST)
	mkdir -p $(SKILL_DST)
	cp -R $(SKILL_SRC)/ $(SKILL_DST)/
	@echo "synced $(SKILL_SRC) -> $(SKILL_DST)"

sync-workflows: ## sync this project's .loom/workflows into ~/.loom/workflows
	mkdir -p $(WF_DST)
	cp -R $(WF_SRC)/ $(WF_DST)/
	@echo "synced $(WF_SRC) -> $(WF_DST)"
