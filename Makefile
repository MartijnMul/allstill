# Everyday commands. `make help` lists them.

# Redocly CLI lints the API description. Pinned; runs in a container, so no
# Node.js is needed on the machine.
REDOCLY := docker run --rm -v "$(CURDIR)/api:/spec:ro" -w /spec redocly/cli:2.11.1

.PHONY: help check api-lint hooks hooks-test secrets

help: ## List the commands
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-11s %s\n", $$1, $$2}'

check: api-lint hooks-test ## Everything CI runs

api-lint: ## Lint the OpenAPI document
	$(REDOCLY) lint openapi.yaml

hooks-test: ## Feed the Claude Code hooks allowed, blocked and edge cases
	python3 -I .claude/hooks/test_hooks.py

hooks: ## Install the git hooks: secrets, private terms, identity, trailers
	git config core.hooksPath .githooks

secrets: ## Scan the whole git history for committed secrets
	gitleaks git --redact --verbose
