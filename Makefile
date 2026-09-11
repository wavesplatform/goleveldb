export GO111MODULE=on

VERSION=$(shell git describe --tags --always --dirty)
SOURCE_DIRS = leveldb manualtest

.PHONY: vendor vetcheck fmtcheck modernize-check clean gotest gotest-issue74 mod-clean

all: vendor vetcheck fmtcheck modernize-check gotest mod-clean

ci: vendor vetcheck fmtcheck modernize-check gotest gotest-issue74 mod-clean

vendor:
	go mod vendor

vetcheck:
	go vet ./...
	golangci-lint run -c .golangci.yml

fmtcheck:
	@gofmt -l -s $(SOURCE_DIRS) | grep ".*\.go"; if [ "$$?" = "0" ]; then exit 1; fi

modernize-check:
	@bash -lc 'set -o pipefail; \
	output=$$(go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest ./... 2>&1 | \
		grep -vE "\.(pb|gen)\.go|mock|_string.go|^exit status|^go: downloading"); \
	[ -n "$$output" ] && { echo "$$output"; exit 1; } || exit 0;'

gotest:
	go test -short -timeout 1h ./...

gotest-issue74:
	go test -timeout 30m -race -run "TestDB_(Concurrent|GoleveldbIssue74)" ./leveldb

mod-clean:
	go mod tidy
