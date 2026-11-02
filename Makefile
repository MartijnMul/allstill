# Everyday commands. `make help` lists them.

# Redocly CLI lints the API description. Pinned; runs in a container, so no
# Node.js is needed on the machine.
REDOCLY := docker run --rm -v "$(CURDIR)/api:/spec:ro" -w /spec redocly/cli:2.11.1

# Go tools are pinned as `tool` directives in go.mod. GoReleaser is not: it
# would add hundreds of modules to go.mod, so it runs pinned by version.
GENERATED := internal/store/internal/db
GENERATED_API := internal/api
GENERATED_CLIENT := internal/apiclient
GORELEASER := go run github.com/goreleaser/goreleaser/v2@v2.18.2

.PHONY: help check go-check fmt-check vet staticcheck test load log-speed vulncheck generate generated-check \
	api-lint hooks hooks-test secrets release-snapshot

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

load: ## The load test of checks Allstill runs itself, before a release
	ALLSTILL_LOAD_TEST=1 go test -count=1 -run '^TestLoad$$' -v ./internal/active

log-speed: ## The log's pages at 3 million entries, before a release that changes them
	ALLSTILL_LOG_SPEED=1 go test -count=1 -timeout 30m -run '^TestTheLogIsQuickAtThreeMillionEntries$$' -v ./internal/store

vulncheck: ## Known vulnerabilities in the code paths we use
	go tool govulncheck ./...

generate: ## Generate the query code with sqlc, and the API's server and client with oapi-codegen
	cd internal/store && go tool sqlc generate
	for f in $(GENERATED)/*.go; do printf '// SPDX-License-Identifier: AGPL-3.0-only\n\n' | cat - "$$f" > "$$f.tmp" && mv "$$f.tmp" "$$f"; done
	cd $(GENERATED_API) && go tool oapi-codegen -config oapi-codegen.yaml ../../api/openapi.yaml
	printf '// SPDX-License-Identifier: AGPL-3.0-only\n\n//lint:file-ignore ST1005 oapi-codegen writes these messages, for headers it could not find\n\n' | cat - $(GENERATED_API)/api.gen.go > $(GENERATED_API)/api.gen.go.tmp && mv $(GENERATED_API)/api.gen.go.tmp $(GENERATED_API)/api.gen.go
	cd $(GENERATED_CLIENT) && go tool oapi-codegen -config oapi-codegen.yaml ../../api/openapi.yaml
	printf '// SPDX-License-Identifier: Apache-2.0\n\n//lint:file-ignore ST1005 oapi-codegen writes these messages\n\n' | cat - $(GENERATED_CLIENT)/client.gen.go > $(GENERATED_CLIENT)/client.gen.go.tmp && mv $(GENERATED_CLIENT)/client.gen.go.tmp $(GENERATED_CLIENT)/client.gen.go

generated-check: ## Fail when generated code is out of date
	@tmp=$$(mktemp -d) && mkdir "$$tmp/db" "$$tmp/api" "$$tmp/client" && cp -R $(GENERATED)/. "$$tmp/db" && cp -R $(GENERATED_API)/. "$$tmp/api" && cp -R $(GENERATED_CLIENT)/. "$$tmp/client" && \
	if ! $(MAKE) -s generate; then rm -rf "$$tmp"; echo "Generating the query code failed."; exit 1; fi; \
	if ! diff -r "$$tmp/db" $(GENERATED) >/dev/null || ! diff -r "$$tmp/api" $(GENERATED_API) >/dev/null || ! diff -r "$$tmp/client" $(GENERATED_CLIENT) >/dev/null; then \
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

release-snapshot: ## Build every release artifact in dist/, without signing or publishing
	$(GORELEASER) release --snapshot --clean --skip=sign,sbom
