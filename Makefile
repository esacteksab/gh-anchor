MAKEFLAGS += --warn-undefined-variables
SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := all
.DELETE_ON_ERROR:
.SUFFIXES:

.PHONY: all
all: audit test build

.PHONY: audit
audit: tidy fmt
	go vet ./...
	@for pkg in $$(go list ./...); do \
		echo "staticcheck $$pkg"; \
		go tool -modfile=go.tool.mod staticcheck "$$pkg" || exit $$?; \
	done
	go tool -modfile=go.tool.mod govulncheck ./...
	golangci-lint run -v


.PHONY: build
build:

	goreleaser build --clean --single-target --snapshot

.PHONY: clean
clean:
ifneq (,$(wildcard ./dist))
	rm -rf dist/

endif

ifneq (,$(wildcard ./coverage))
	rm -rf coverage/

endif

.PHONY: coverage
coverage:
	@mkdir -p coverage
	go test ./... -covermode=atomic -coverprofile=coverage/coverage.out
	go tool cover -html=coverage/coverage.out -o coverage/coverage.html
	@echo "Coverage report generated: coverage/coverage.html"

.PHONY: fmt
fmt:
	go tool -modfile=go.tool.mod golines --base-formatter=gofumpt -w .
	go tool -modfile=go.tool.mod gofumpt -l -w -extra .

.PHONY: install
install: build

	cp dist/gh-anchor_*/gh-anchor .

	-gh ext remove anchor

	gh ext install .

	-gh anchor --version

.PHONY: lint
lint:
	golangci-lint run -v

.PHONY: test
test:
	go test ./... -cover

.PHONY: test-race
test-race:
	go test ./... -race -count=1

.PHONY: modernize
modernize:
	go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest -fix -test ./...

.PHONY: tidy
tidy:
	go mod tidy
	go mod tidy -modfile=go.tool.mod

.PHONY: update-tools
update-tools:
	go get -u -modfile=go.tool.mod honnef.co/go/tools/cmd/staticcheck
	go get -u -modfile=go.tool.mod golang.org/x/vuln/cmd/govulncheck
	go get -u -modfile=go.tool.mod github.com/segmentio/golines
	go get -u -modfile=go.tool.mod mvdan.cc/gofumpt
	go mod tidy -modfile=go.tool.mod

.PHONY: update
update: update-tools
	go get -u ./...
	go mod tidy
