BIN := cogitator
SCHEMA_PORT := 17777
SCHEMA_URL := http://127.0.0.1:$(SCHEMA_PORT)/doc

.PHONY: test lint build run vet ci clean release generate capture-schema demo

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run

build:
	go build -o $(BIN) ./cmd/cogitator

run:
	go run ./cmd/cogitator

demo: build
	vhs demo.tape
	cp -f tui.png docs/public/tui.png

vet:
	go vet ./...

ci: vet lint test
	GOOS=linux go build ./...
	GOOS=darwin go build ./...

generate:
	go tool oapi-codegen --config internal/oc/oapi-codegen.yaml internal/oc/openapi.json

capture-schema:
	@command -v opencode >/dev/null 2>&1 || (echo "opencode must be on PATH" && exit 1)
	@pid_file=$$(mktemp); raw_schema=$$(mktemp); \
		opencode serve --port $(SCHEMA_PORT) >/tmp/cogitator-schema.log 2>&1 & \
		echo $$! > $$pid_file; \
		trap 'kill $$(cat $$pid_file) 2>/dev/null || true; rm -f $$pid_file $$raw_schema' EXIT INT TERM; \
		i=0; \
		while [ $$i -lt 40 ]; do \
			if curl -fsS "$(SCHEMA_URL)" -o $$raw_schema; then \
				if jq -e '.components.schemas.Session and .components.schemas.PermissionRequest and .components.schemas.Event' $$raw_schema >/dev/null; then \
					version=$$(jq -r '.info.version // "1.0.0"' $$raw_schema); \
					tmp=$$(mktemp); \
					jq --arg version "$$version" '.info.version = $$version' internal/oc/openapi.json > $$tmp; \
					mv $$tmp internal/oc/openapi.json; \
					echo "captured schema from $(SCHEMA_URL) and updated internal/oc/openapi.json"; \
					exit 0; \
				fi; \
				echo "captured schema is missing required components"; \
				exit 1; \
			fi; \
			sleep 0.25; \
			i=$$((i + 1)); \
		done; \
		echo "failed to fetch $(SCHEMA_URL)"; \
		exit 1

release:
	goreleaser release --clean

clean:
	rm -f $(BIN) coverage.out
	rm -rf dist
