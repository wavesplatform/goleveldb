// Copyright (c) 2012, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package leveldb

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/wavesplatform/goleveldb/leveldb/iterator"
	"github.com/wavesplatform/goleveldb/leveldb/opt"
	"github.com/wavesplatform/goleveldb/leveldb/storage"
	"github.com/wavesplatform/goleveldb/leveldb/util"
)

func randomString(r *rand.Rand, n int) []byte {
	b := new(bytes.Buffer)
	for range n {
		b.WriteByte(' ' + byte(r.Intn(95)))
	}
	return b.Bytes()
}

func compressibleStr(r *rand.Rand, frac float32, n int) []byte {
	nn := int(float32(n) * frac)
	rb := randomString(r, nn)
	b := make([]byte, 0, n+nn)
	for len(b) < n {
		b = append(b, rb...)
	}
	return b[:n]
}

type valueGen struct {
	src []byte
	pos int
}

func newValueGen(frac float32) *valueGen {
	v := new(valueGen)
	r := rand.New(rand.NewSource(301))
	v.src = make([]byte, 0, 1048576+100)
	for len(v.src) < 1048576 {
		v.src = append(v.src, compressibleStr(r, frac, 100)...)
	}
	return v
}

func (v *valueGen) get(n int) []byte {
	if v.pos+n > len(v.src) {
		v.pos = 0
	}
	v.pos += n
	return v.src[v.pos-n : v.pos]
}

var benchDB = filepath.Join(os.TempDir(), fmt.Sprintf("goleveldbbench-%d", os.Getuid()))

type dbBench struct {
	b    *testing.B
	stor storage.Storage
	db   *DB

	o  *opt.Options
	ro *opt.ReadOptions
	wo *opt.WriteOptions

	keys, values [][]byte
}

func openDBBench(b *testing.B, noCompress bool) *dbBench {
	_, err := os.Stat(benchDB)
	if err == nil {
		err = os.RemoveAll(benchDB)
		if err != nil {
			b.Fatal("cannot remove old db: ", err)
		}
	}

	p := &dbBench{
		b:  b,
		o:  &opt.Options{},
		ro: &opt.ReadOptions{},
		wo: &opt.WriteOptions{},
	}
	p.stor, err = storage.OpenFile(benchDB, false)
	if err != nil {
		b.Fatal("cannot open stor: ", err)
	}
	if noCompress {
		p.o.Compression = opt.NoCompression
	}

	p.db, err = Open(p.stor, p.o)
	if err != nil {
		b.Fatal("cannot open db: ", err)
	}

	return p
}

func (p *dbBench) reopen() {
	p.db.Close()
	var err error
	p.db, err = Open(p.stor, p.o)
	if err != nil {
		p.b.Fatal("Reopen: got error: ", err)
	}
}

// waitCompaction waits until no table compaction is pending or running.
// compTriggerWait alone returns as soon as writes may resume, which can be
// while compactions are still running.
func (p *dbBench) waitCompaction() {
	for {
		// The command is received only between compactions, so once no
		// compaction is needed, the acknowledgement means the table
		// compaction goroutine is idle.
		need := p.db.tableNeedCompaction()
		if err := p.db.compTriggerWait(p.db.tcompCmdC); err != nil {
			p.b.Fatal("table compaction failed: ", err)
		}
		if !need {
			return
		}
	}
}

func (p *dbBench) populate(n int) {
	p.keys, p.values = make([][]byte, n), make([][]byte, n)
	v := newValueGen(0.5)
	for i := range p.keys {
		p.keys[i], p.values[i] = fmt.Appendf(nil, "%016d", i), v.get(100)
	}
}

func (p *dbBench) randomize() {
	m := len(p.keys)
	times := m * 2
	r1, r2 := rand.New(rand.NewSource(0xdeadbeef)), rand.New(rand.NewSource(0xbeefface))
	for range times {
		i, j := r1.Int()%m, r2.Int()%m
		if i == j {
			continue
		}
		p.keys[i], p.keys[j] = p.keys[j], p.keys[i]
		p.values[i], p.values[j] = p.values[j], p.values[i]
	}
}

func (p *dbBench) writes(perBatch int) {
	b := p.b
	db := p.db

	n := len(p.keys)
	m := n / perBatch
	if n%perBatch > 0 {
		m++
	}
	batches := make([]Batch, m)
	j := 0
	for i := range batches {
		first := true
		for ; j < n && ((j+1)%perBatch != 0 || first); j++ {
			first = false
			batches[i].Put(p.keys[j], p.values[j])
		}
	}
	runtime.GC()

	b.ResetTimer()
	b.StartTimer()
	for i := range batches {
		err := db.Write(&(batches[i]), p.wo)
		if err != nil {
			b.Fatal("write failed: ", err)
		}
	}
	b.StopTimer()
	b.SetBytes(116)
}

func (p *dbBench) gc() {
	p.keys, p.values = nil, nil
	runtime.GC()
}

func (p *dbBench) puts() {
	b := p.b
	db := p.db

	b.ResetTimer()
	b.StartTimer()
	for i := range p.keys {
		err := db.Put(p.keys[i], p.values[i], p.wo)
		if err != nil {
			b.Fatal("put failed: ", err)
		}
	}
	b.StopTimer()
	b.SetBytes(116)
}

func (p *dbBench) fill() {
	b := p.b
	db := p.db

	perBatch := 10000
	batch := new(Batch)
	for i, n := 0, len(p.keys); i < n; {
		first := true
		for ; i < n && ((i+1)%perBatch != 0 || first); i++ {
			first = false
			batch.Put(p.keys[i], p.values[i])
		}
		err := db.Write(batch, p.wo)
		if err != nil {
			b.Fatal("write failed: ", err)
		}
		batch.Reset()
	}
}

func (p *dbBench) gets() {
	b := p.b
	db := p.db

	b.ResetTimer()
	for i := range p.keys {
		_, err := db.Get(p.keys[i], p.ro)
		if err != nil {
			b.Error("got error: ", err)
		}
	}
	b.StopTimer()
}

func (p *dbBench) seeks() {
	b := p.b

	iter := p.newIter()
	defer iter.Release()
	b.ResetTimer()
	for i := range p.keys {
		if !iter.Seek(p.keys[i]) {
			b.Error("value not found for: ", string(p.keys[i]))
		}
	}
	b.StopTimer()
}

func (p *dbBench) newIter() iterator.Iterator {
	iter := p.db.NewIterator(nil, p.ro)
	err := iter.Error()
	if err != nil {
		p.b.Fatal("cannot create iterator: ", err)
	}
	return iter
}

func (p *dbBench) close() {
	if bp, err := p.db.GetProperty("leveldb.blockpool"); err == nil {
		p.b.Log("Block pool stats: ", bp)
	}
	p.db.Close()
	p.stor.Close()
	os.RemoveAll(benchDB)
	p.db = nil
	p.keys = nil
	p.values = nil
	runtime.GC()
}

func BenchmarkDBWrite(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.writes(1)
	p.close()
}

func BenchmarkDBWriteBatch(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.writes(1000)
	p.close()
}

func BenchmarkDBWriteUncompressed(b *testing.B) {
	p := openDBBench(b, true)
	p.populate(b.N)
	p.writes(1)
	p.close()
}

func BenchmarkDBWriteBatchUncompressed(b *testing.B) {
	p := openDBBench(b, true)
	p.populate(b.N)
	p.writes(1000)
	p.close()
}

func BenchmarkDBWriteRandom(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.randomize()
	p.writes(1)
	p.close()
}

func BenchmarkDBWriteRandomSync(b *testing.B) {
	p := openDBBench(b, false)
	p.wo.Sync = true
	p.populate(b.N)
	p.writes(1)
	p.close()
}

func BenchmarkDBOverwrite(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.writes(1)
	p.writes(1)
	p.close()
}

func BenchmarkDBOverwriteRandom(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.writes(1)
	p.randomize()
	p.writes(1)
	p.close()
}

func BenchmarkDBPut(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.puts()
	p.close()
}

func BenchmarkDBRead(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.gc()

	iter := p.newIter()
	b.ResetTimer()
	for iter.Next() {
	}
	iter.Release()
	b.StopTimer()
	b.SetBytes(116)
	p.close()
}

func BenchmarkDBReadGC(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()

	iter := p.newIter()
	b.ResetTimer()
	for iter.Next() {
	}
	iter.Release()
	b.StopTimer()
	b.SetBytes(116)
	p.close()
}

func BenchmarkDBReadUncompressed(b *testing.B) {
	p := openDBBench(b, true)
	p.populate(b.N)
	p.fill()
	p.gc()

	iter := p.newIter()
	b.ResetTimer()
	for iter.Next() {
	}
	iter.Release()
	b.StopTimer()
	b.SetBytes(116)
	p.close()
}

func BenchmarkDBReadTable(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.reopen()
	p.gc()

	iter := p.newIter()
	b.ResetTimer()
	for iter.Next() {
	}
	iter.Release()
	b.StopTimer()
	b.SetBytes(116)
	p.close()
}

func BenchmarkDBReadReverse(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.gc()

	iter := p.newIter()
	b.ResetTimer()
	iter.Last()
	for iter.Prev() {
	}
	iter.Release()
	b.StopTimer()
	b.SetBytes(116)
	p.close()
}

func BenchmarkDBReadReverseTable(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.reopen()
	p.gc()

	iter := p.newIter()
	b.ResetTimer()
	iter.Last()
	for iter.Prev() {
	}
	iter.Release()
	b.StopTimer()
	b.SetBytes(116)
	p.close()
}

func BenchmarkDBSeek(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.seeks()
	p.close()
}

func BenchmarkDBSeekRandom(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.randomize()
	p.seeks()
	p.close()
}

func BenchmarkDBGet(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.gets()
	p.close()
}

func BenchmarkDBGetRandom(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.randomize()
	p.gets()
	p.close()
}

func BenchmarkDBReadConcurrent(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.gc()
	defer p.close()

	b.ResetTimer()
	b.SetBytes(116)

	b.RunParallel(func(pb *testing.PB) {
		iter := p.newIter()
		defer iter.Release()
		for pb.Next() && iter.Next() {
		}
	})
}

func BenchmarkDBReadConcurrent2(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.gc()
	defer p.close()

	b.ResetTimer()
	b.SetBytes(116)

	var dir atomic.Uint32
	b.RunParallel(func(pb *testing.PB) {
		iter := p.newIter()
		defer iter.Release()
		if dir.Add(1)%2 == 0 {
			for pb.Next() && iter.Next() {
			}
		} else {
			if pb.Next() && iter.Last() {
				for pb.Next() && iter.Prev() {
				}
			}
		}
	})
}

func BenchmarkDBCompactRange(b *testing.B) {
	p := openDBBench(b, false)
	p.populate(b.N)
	p.fill()
	p.gc()

	b.ResetTimer()
	if err := p.db.CompactRange(util.Range{}); err != nil {
		b.Fatal("compact range failed: ", err)
	}
	b.StopTimer()
	b.SetBytes(116)
	p.close()
}

// BenchmarkDBCompactTables measures compaction of tables only: the memdb is
// flushed to level-0 by reopening the DB, and pending table compactions are
// done, before the timer starts.
func BenchmarkDBCompactTables(b *testing.B) {
	p := openDBBench(b, false)
	p.o.DisableSeeksCompaction = true
	p.reopen()
	p.populate(b.N)
	p.fill()
	p.reopen()
	p.waitCompaction()
	p.gc()

	b.ResetTimer()
	if err := p.db.CompactRange(util.Range{}); err != nil {
		b.Fatal("compact range failed: ", err)
	}
	b.StopTimer()
	b.SetBytes(116)
	p.close()
}

// benchKeysLen is the size of the fixed data set used by the per-level
// benchmarks below, which run b.N operations over it.
const benchKeysLen = 100000

// Where the fixed data set is placed by openLevelBench.
const (
	benchInMemdb = iota
	benchInLevel0
	benchInDeepLevel
)

// openLevelBench opens a DB with benchKeysLen keys placed in the memdb, in a
// single level-0 table or in the deepest level, so that the read paths can be
// benchmarked separately. Keys are shuffled for random access afterwards.
func openLevelBench(b *testing.B, where int) *dbBench {
	p := openDBBench(b, false)
	p.o.WriteBuffer = 64 * opt.MiB
	p.o.DisableSeeksCompaction = true
	p.reopen()
	p.populate(benchKeysLen)
	p.fill()

	switch where {
	case benchInMemdb:
	case benchInLevel0:
		// Recovering the journal flushes the memdb into level-0.
		p.reopen()
		if n := p.db.s.tLen(0); n != 1 {
			b.Fatalf("expected 1 table at level-0, got %d", n)
		}
	case benchInDeepLevel:
		if err := p.db.CompactRange(util.Range{}); err != nil {
			b.Fatal("compact range failed: ", err)
		}
		p.reopen()
		v := p.db.s.version()
		n0, nl := v.tLen(0), len(v.levels)
		v.release()
		if n0 != 0 || nl < 2 {
			b.Fatalf("expected tables below level-0, got %d tables at level-0 and %d levels", n0, nl)
		}
	default:
		b.Fatalf("unknown placement %d", where)
	}
	p.randomize()
	runtime.GC()
	return p
}

func (p *dbBench) getsN() {
	b := p.b
	db := p.db

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if _, err := db.Get(p.keys[i%len(p.keys)], p.ro); err != nil {
			b.Fatal("got error: ", err)
		}
	}
	b.StopTimer()
}

func (p *dbBench) seeksN(nexts int) {
	b := p.b

	iter := p.newIter()
	defer iter.Release()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		key := p.keys[i%len(p.keys)]
		if !iter.Seek(key) {
			b.Fatal("value not found for: ", string(key))
		}
		for j := 0; j < nexts && iter.Next(); j++ {
		}
	}
	b.StopTimer()
	if err := iter.Error(); err != nil {
		b.Fatal("iterator error: ", err)
	}
}

func BenchmarkDBGetMemdb(b *testing.B) {
	p := openLevelBench(b, benchInMemdb)
	p.getsN()
	p.close()
}

func BenchmarkDBGetLevel0(b *testing.B) {
	p := openLevelBench(b, benchInLevel0)
	p.getsN()
	p.close()
}

func BenchmarkDBGetDeepLevel(b *testing.B) {
	p := openLevelBench(b, benchInDeepLevel)
	p.getsN()
	p.close()
}

func BenchmarkDBSeekMemdb(b *testing.B) {
	p := openLevelBench(b, benchInMemdb)
	p.seeksN(0)
	p.close()
}

func BenchmarkDBSeekLevel0(b *testing.B) {
	p := openLevelBench(b, benchInLevel0)
	p.seeksN(0)
	p.close()
}

func BenchmarkDBSeekDeepLevel(b *testing.B) {
	p := openLevelBench(b, benchInDeepLevel)
	p.seeksN(0)
	p.close()
}

func BenchmarkDBSeekNextMemdb(b *testing.B) {
	p := openLevelBench(b, benchInMemdb)
	p.seeksN(10)
	p.close()
}

func BenchmarkDBSeekNextDeepLevel(b *testing.B) {
	p := openLevelBench(b, benchInDeepLevel)
	p.seeksN(10)
	p.close()
}
