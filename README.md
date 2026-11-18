This is an implementation of the [LevelDB key/value database](https://github.com/google/leveldb) in the [Go programming language](https://go.dev).
Forked from [syndtr/goleveldb](https://github.com/syndtr/goleveldb).

[//]: # ([![Build Status]&#40;https://app.travis-ci.com/syndtr/goleveldb.svg?branch=master&#41;]&#40;https://app.travis-ci.com/syndtr/goleveldb&#41;)

Installation
-----------

	go get github.com/wavesplatform/goleveldb

Requirements
-----------

* Requires Go 1.27 or newer.

Usage
-----------

Create or open a database:
```go
// The returned DB instance is safe for concurrent use. Which mean that all
// DB's methods may be called concurrently from multiple goroutine.
db, err := leveldb.OpenFile("path/to/db", nil)
...
defer db.Close()
...
```
Read or modify the database content:
```go
// Remember that the contents of the returned slice should not be modified.
data, err := db.Get([]byte("key"), nil)
...
err = db.Put([]byte("key"), []byte("value"), nil)
...
err = db.Delete([]byte("key"), nil)
...
```

Iterate over database content:
```go
iter := db.NewIterator(nil, nil)
for iter.Next() {
	// Remember that the contents of the returned slice should not be modified, and
	// only valid until the next call to Next.
	key := iter.Key()
	value := iter.Value()
	...
}
iter.Release()
err = iter.Error()
...
```
Seek-then-Iterate:
```go
iter := db.NewIterator(nil, nil)
for ok := iter.Seek(key); ok; ok = iter.Next() {
	// Use key/value.
	...
}
iter.Release()
err = iter.Error()
...
```
Iterate over subset of database content:
```go
iter := db.NewIterator(&util.Range{Start: []byte("foo"), Limit: []byte("xoo")}, nil)
for iter.Next() {
	// Use key/value.
	...
}
iter.Release()
err = iter.Error()
...
```
Iterate over subset of database content with a particular prefix:
```go
iter := db.NewIterator(util.BytesPrefix([]byte("foo-")), nil)
for iter.Next() {
	// Use key/value.
	...
}
iter.Release()
err = iter.Error()
...
```
Batch writes:
```go
batch := new(leveldb.Batch)
batch.Put([]byte("foo"), []byte("value"))
batch.Put([]byte("bar"), []byte("another value"))
batch.Delete([]byte("baz"))
err = db.Write(batch, nil)
...
```
Use bloom filter:
```go
o := &opt.Options{
	Filter: filter.NewBloomFilter(10),
}
db, err := leveldb.OpenFile("path/to/db", o)
...
defer db.Close()
...
```
Benchmarking and profiling
-----------

The Makefile has targets for benchmarking and profiling; it requires bash
(`/bin/bash`). Each target accepts `PKG` (default `./leveldb/...`), `BENCH`
(benchmark regexp, default `.`) and `COUNT` (default `1`). Other settings are
`BENCH_TIMEOUT` (default `2h`), `BENCH_FLAGS` (extra `go test` flags, e.g.
`BENCH_FLAGS=-benchtime=2s`) and `BENCH_OUT` (the results file written by
`bench` and read by `bench-compare`, default `build/bench/current.txt`).
Output goes to `build/`.

* `make bench` runs the benchmarks with `-benchmem` and writes the results to
  `build/bench/current.txt`, e.g. `make bench PKG=./leveldb BENCH=DBGet COUNT=10`.
* `make bench-baseline` runs the benchmarks with `COUNT=10` and records them in
  `build/bench/baseline.txt`.
* `make bench-compare` compares `build/bench/current.txt` with the baseline
  using [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat).
* `make bench-profile` runs the benchmarks of a single package and collects
  CPU, memory, block and mutex profiles in `build/profile/<pkg>/`, then prints
  the top `PPROF_TOP` (default `25`) entries of each, e.g.
  `make bench-profile PKG=./leveldb/memdb BENCH=Put`. `PKG` must name a single
  package here and for `flamegraph`, the default `./leveldb/...` is rejected.
* `make flamegraph` opens a collected profile in the pprof web UI (flame graph
  view), e.g. `make flamegraph PKG=./leveldb/memdb PROFILE=mem`. `PROFILE` is
  one of `cpu` (default), `mem`, `block` or `mutex`, and `PPROF_PORT` sets the
  port (default `8080`).

A typical workflow: `make bench-baseline`, change the code, `make bench COUNT=10`,
then `make bench-compare`.

Documentation
-----------

You can read package documentation [here](https://pkg.go.dev/github.com/wavesplatform/goleveldb).
