# Everyday commands. `make help` lists them.

# Redocly CLI lints the API description. Pinned; runs in a container, so no
# Node.js is needed on the machine.
REDOCLY := docker run --rm -v "$(CURDIR)/api:/spec:ro" -w /spec redocly/cli:2.11.1

# Go tools are pinned as `tool` directives in go.mod.
GENERATED := internal/store/internal/db

.PHONY: help check go-check fmt-check vet staticcheck test vulncheck generate generated-check \
	api-lint hooks hooks-test secrets

help: ## List the commands
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

check: api-lint hooks-test go-check ## Everything CI runs

go-check: fmt-check vet staticcheck test generated-check vulncheck ## Every Go check

fmt-check: ## Fail on Go files gofmt would change
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt would change:"; echo "$$out"; exit 1; fi

vet: ## go vet
	go vet ./...

staticcheck: ## staticcheck
	go tool staticcheck ./...

test: ## The tests, with the race detector
	go test -race ./...

vulncheck: ## Known vulnerabilities in the code paths we use
	go tool govulncheck ./...

generate: ## Generate the query code with sqlc
	cd internal/store && go tool sqlc generate
	for f in $(GENERATED)/*.go; do printf '// SPDX-License-Identifier: AGPL-3.0-only\n\n' | cat - "$$f" > "$$f.tmp" && mv "$$f.tmp" "$$f"; done

generated-check: ## Fail when generated code is out of date
	@tmp=$$(mktemp -d) && cp -R $(GENERATED)/. "$$tmp" && $(MAKE) -s generate && \
	if ! diff -r "$$tmp" $(GENERATED) >/dev/null; then \
		echo "Generated code was out of date; run 'make generate' and commit the result."; rm -rf "$$tmp"; exit 1; \
	fi; rm -rf "$$tmp"; echo "generated code is current"

api-lint: ## Lint the OpenAPI document
	$(REDOCLY) lint openapi.yaml

hooks-test: ## Feed the Claude Code hooks allowed, blocked and edge cases
	python3 -I .claude/hooks/test_hooks.py

hooks: ## Install the git hooks: secrets, private terms, identity, trailers
	git config core.hooksPath .githooks

secrets: ## Scan the whole git history for committed secrets
	gitleaks git --redact --verbose
