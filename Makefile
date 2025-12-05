export GO111MODULE=on

VERSION=$(shell git describe --tags --always --dirty)
SOURCE_DIRS = leveldb manualtest

.PHONY: vendor vetcheck fmtcheck clean gotest gotest-issue74 mod-clean

all: vendor vetcheck fmtcheck gotest mod-clean

ci: vendor vetcheck fmtcheck gotest gotest-issue74 mod-clean

vendor:
	go mod vendor

vetcheck:
	go vet ./...
	golangci-lint run -c .golangci.yml

fmtcheck:
	@gofmt -l -s $(SOURCE_DIRS) | grep ".*\.go"; if [ "$$?" = "0" ]; then exit 1; fi

gotest:
	go test -short -timeout 1h ./...

gotest-issue74:
	go test -timeout 30m -race -run "TestDB_(Concurrent|GoleveldbIssue74)" ./leveldb

mod-clean:
	go mod tidy
