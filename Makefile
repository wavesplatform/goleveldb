export GO111MODULE=on

# bash is required for "set -o pipefail" in the benchmark targets.
SHELL := /bin/bash

VERSION=$(shell git describe --tags --always --dirty)
SOURCE_DIRS = leveldb manualtest

# Benchmarking and profiling settings, override on the command line:
#   make bench PKG=./leveldb/memdb BENCH=Put COUNT=10
#   make bench-profile PKG=./leveldb/memdb BENCH=Put
#   make flamegraph PKG=./leveldb/memdb PROFILE=mem
PKG ?= ./leveldb/...
BENCH ?= .
COUNT ?= 1
BENCH_TIMEOUT ?= 2h
BENCH_FLAGS ?=
BENCH_DIR = build/bench
BENCH_OUT ?= $(BENCH_DIR)/current.txt
BENCH_BASELINE = $(BENCH_DIR)/baseline.txt
BENCHSTAT_VERSION = v0.0.0-20261009192801-be2c69fb417e
PROFILE ?= cpu
PPROF_PORT ?= 8080
PPROF_TOP ?= 25
PROFILE_PKG = $(patsubst ./%,%,$(PKG))
PROFILE_DIR = build/profile/$(PROFILE_PKG)
PROFILE_BIN = $(PROFILE_DIR)/$(notdir $(abspath $(PKG))).test

.PHONY: vendor vetcheck fmtcheck modernize-check clean gotest gotest-issue74 mod-clean \
	bench bench-profile flamegraph bench-baseline bench-compare profile-pkg-check

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
	go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@v0.50.0 ./...

gotest:
	go test -short -timeout 1h ./...

gotest-issue74:
	go test -timeout 30m -race -run "TestDB_(Concurrent|GoleveldbIssue74)" ./leveldb

mod-clean:
	go mod tidy

bench:
	@mkdir -p $(dir $(BENCH_OUT))
	set -o pipefail; go test -run='^$$' -bench='$(BENCH)' -benchmem -count=$(COUNT) -timeout $(BENCH_TIMEOUT) $(BENCH_FLAGS) $(PKG) | tee $(BENCH_OUT)

bench-baseline:
	$(MAKE) bench COUNT=10 BENCH_OUT=$(BENCH_BASELINE)

bench-compare:
	@test -f $(BENCH_BASELINE) || { echo "missing $(BENCH_BASELINE), run 'make bench-baseline' first"; exit 1; }
	@test -f $(BENCH_OUT) || { echo "missing $(BENCH_OUT), run 'make bench COUNT=10' first"; exit 1; }
	go run golang.org/x/perf/cmd/benchstat@$(BENCHSTAT_VERSION) $(BENCH_BASELINE) $(BENCH_OUT)

profile-pkg-check:
	@case '$(PKG)' in *...*) echo "PKG must be a single package for profiling, e.g. PKG=./leveldb/memdb"; exit 1;; esac

bench-profile: profile-pkg-check
	@mkdir -p $(PROFILE_DIR)
	set -o pipefail; go test -run='^$$' -bench='$(BENCH)' -benchmem -count=$(COUNT) -timeout $(BENCH_TIMEOUT) $(BENCH_FLAGS) \
		-o $(PROFILE_BIN) \
		-cpuprofile $(PROFILE_DIR)/cpu.prof \
		-memprofile $(PROFILE_DIR)/mem.prof \
		-blockprofile $(PROFILE_DIR)/block.prof \
		-mutexprofile $(PROFILE_DIR)/mutex.prof \
		$(PKG) | tee $(PROFILE_DIR)/bench.txt
	@echo "=== CPU (top $(PPROF_TOP)) ==="
	@go tool pprof -top -nodecount=$(PPROF_TOP) $(PROFILE_BIN) $(PROFILE_DIR)/cpu.prof
	@echo "=== Allocated space (top $(PPROF_TOP)) ==="
	@go tool pprof -top -nodecount=$(PPROF_TOP) -sample_index=alloc_space $(PROFILE_BIN) $(PROFILE_DIR)/mem.prof
	@echo "=== Allocated objects (top $(PPROF_TOP)) ==="
	@go tool pprof -top -nodecount=$(PPROF_TOP) -sample_index=alloc_objects $(PROFILE_BIN) $(PROFILE_DIR)/mem.prof
	@echo "=== Blocking (top $(PPROF_TOP)) ==="
	@go tool pprof -top -nodecount=$(PPROF_TOP) $(PROFILE_BIN) $(PROFILE_DIR)/block.prof || true
	@echo "=== Mutex contention (top $(PPROF_TOP)) ==="
	@go tool pprof -top -nodecount=$(PPROF_TOP) $(PROFILE_BIN) $(PROFILE_DIR)/mutex.prof || true

flamegraph: profile-pkg-check
	@test -f $(PROFILE_DIR)/$(PROFILE).prof || { echo "missing $(PROFILE_DIR)/$(PROFILE).prof, run 'make bench-profile PKG=$(PKG)' first"; exit 1; }
	go tool pprof -http=:$(PPROF_PORT) $(PROFILE_BIN) $(PROFILE_DIR)/$(PROFILE).prof
