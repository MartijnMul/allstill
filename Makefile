# Everyday commands. `make help` lists them.

# Redocly CLI lints the API description. Pinned; runs in a container, so no
# Node.js is needed on the machine.
REDOCLY := docker run --rm -v "$(CURDIR)/api:/spec:ro" -w /spec redocly/cli:2.11.1

.PHONY: help api-lint

help: ## List the commands
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

api-lint: ## Lint the OpenAPI document
	$(REDOCLY) lint openapi.yaml
