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
	"testing"
	"testing/quick"

	"github.com/wavesplatform/goleveldb/leveldb/comparer"
	"github.com/wavesplatform/goleveldb/leveldb/memdb"
	"github.com/wavesplatform/goleveldb/leveldb/testutil"
)

func TestBatchHeader(t *testing.T) {
	f := func(seq uint64, length uint32) bool {
		encoded := encodeBatchHeader(nil, seq, int(length))
		decSeq, decLength, err := decodeBatchHeader(encoded)
		return err == nil && decSeq == seq && decLength == int(length)
	}
	config := &quick.Config{
		Rand: testutil.NewRand(),
	}
	if err := quick.Check(f, config); err != nil {
		t.Error(err)
	}
}

type batchKV struct {
	kt   keyType
	k, v []byte
}

func TestBatch(t *testing.T) {
	var (
		kvs         []batchKV
		internalLen int
	)
	batch := new(Batch)
	rbatch := new(Batch)
	abatch := new(Batch)
	testBatch := func(i int, kt keyType, k, v []byte) error {
		kv := kvs[i]
		if kv.kt != kt {
			return fmt.Errorf("invalid key type, index=%d: %d vs %d", i, kv.kt, kt)
		}
		if !bytes.Equal(kv.k, k) {
			return fmt.Errorf("invalid key, index=%d", i)
		}
		if !bytes.Equal(kv.v, v) {
			return fmt.Errorf("invalid value, index=%d", i)
		}
		return nil
	}
	f := func(ktr uint8, k, v []byte) bool {
		kt := keyType(ktr % 2)
		if kt == keyTypeVal {
			batch.Put(k, v)
			rbatch.Put(k, v)
			kvs = append(kvs, batchKV{kt: kt, k: k, v: v})
			internalLen += len(k) + len(v) + 8
		} else {
			batch.Delete(k)
			rbatch.Delete(k)
			kvs = append(kvs, batchKV{kt: kt, k: k})
			internalLen += len(k) + 8
		}
		if batch.Len() != len(kvs) {
			t.Logf("batch.Len: %d vs %d", len(kvs), batch.Len())
			return false
		}
		if batch.internalLen != internalLen {
			t.Logf("abatch.internalLen: %d vs %d", internalLen, batch.internalLen)
			return false
		}
		if len(kvs)%1000 == 0 {
			if err := batch.replayInternal(testBatch); err != nil {
				t.Logf("batch.replayInternal: %v", err)
				return false
			}

			abatch.append(rbatch)
			rbatch.Reset()
			if abatch.Len() != len(kvs) {
				t.Logf("abatch.Len: %d vs %d", len(kvs), abatch.Len())
				return false
			}
			if abatch.internalLen != internalLen {
				t.Logf("abatch.internalLen: %d vs %d", internalLen, abatch.internalLen)
				return false
			}
			if err := abatch.replayInternal(testBatch); err != nil {
				t.Logf("abatch.replayInternal: %v", err)
				return false
			}

			nbatch := new(Batch)
			if err := nbatch.Load(batch.Dump()); err != nil {
				t.Logf("nbatch.Load: %v", err)
				return false
			}
			if nbatch.Len() != len(kvs) {
				t.Logf("nbatch.Len: %d vs %d", len(kvs), nbatch.Len())
				return false
			}
			if nbatch.internalLen != internalLen {
				t.Logf("nbatch.internalLen: %d vs %d", internalLen, nbatch.internalLen)
				return false
			}
			if err := nbatch.replayInternal(testBatch); err != nil {
				t.Logf("nbatch.replayInternal: %v", err)
				return false
			}
		}
		if len(kvs)%10000 == 0 {
			nbatch := new(Batch)
			if err := batch.Replay(nbatch); err != nil {
				t.Logf("batch.Replay: %v", err)
				return false
			}
			if nbatch.Len() != len(kvs) {
				t.Logf("nbatch.Len: %d vs %d", len(kvs), nbatch.Len())
				return false
			}
			if nbatch.internalLen != internalLen {
				t.Logf("nbatch.internalLen: %d vs %d", internalLen, nbatch.internalLen)
				return false
			}
			if err := nbatch.replayInternal(testBatch); err != nil {
				t.Logf("nbatch.replayInternal: %v", err)
				return false
			}
		}
		return true
	}
	config := &quick.Config{
		MaxCount: 40000,
		Rand:     testutil.NewRand(),
	}
	if err := quick.Check(f, config); err != nil {
		t.Error(err)
	}
	t.Logf("length=%d internalLen=%d", len(kvs), internalLen)
}

func BenchmarkDefaultBatchWrite(b *testing.B) {
	benchmarkBatchWrite(b, nil)
}

func BenchmarkFastAllocationBatchWrite(b *testing.B) {
	benchmarkBatchWrite(b, &BatchConfig{
		GrowLimit: 10 * batchGrowLimit,
	})
}

func benchmarkBatchWrite(b *testing.B, config *BatchConfig) {
	var (
		keys [][]byte
		vals [][]byte
		r    = rand.New(rand.NewSource(1337))
	)
	for range 50000 {
		keys = append(keys, randomString(r, 32))
		vals = append(vals, randomString(r, 100))
	}

	for b.Loop() {
		batch := MakeBatchWithConfig(config)
		for i := 0; i < len(keys); i++ {
			batch.Put(keys[i], vals[i])
		}
	}
	b.ReportAllocs()
}

// BenchmarkBatchPutSmall builds a new small batch per iteration, like a
// caller grouping a few related writes.
func BenchmarkBatchPutSmall(b *testing.B) {
	for _, n := range []int{1, 10, 100} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			r := rand.New(rand.NewSource(1337))
			keys := make([][]byte, n)
			vals := make([][]byte, n)
			for i := range keys {
				keys[i] = randomString(r, 16)
				vals[i] = randomString(r, 100)
			}
			b.ReportAllocs()
			for b.Loop() {
				batch := new(Batch)
				for i := range keys {
					batch.Put(keys[i], vals[i])
				}
			}
		})
	}
}

// TestBatchGrow checks the batch records while the data and index buffers
// grow, with the default and small grow limits.
func TestBatchGrow(t *testing.T) {
	type rec struct {
		kt   keyType
		k, v []byte
	}
	for _, limit := range []int{0, 1, 7, 100} {
		r := rand.New(rand.NewSource(int64(limit)))
		b := MakeBatchWithConfig(&BatchConfig{GrowLimit: limit})
		var recs []rec
		for i := range 5000 {
			// Mix record sizes so that the index estimate is sometimes off.
			k := randomString(r, 1+r.Intn(40))
			if i%3 == 0 {
				b.Delete(k)
				recs = append(recs, rec{keyTypeDel, k, nil})
				continue
			}
			v := randomString(r, r.Intn(300))
			b.Put(k, v)
			recs = append(recs, rec{keyTypeVal, k, v})
		}
		check := func(name string, b *Batch) {
			t.Helper()
			if b.Len() != len(recs) {
				t.Fatalf("limit %d %s: got %d records, want %d", limit, name, b.Len(), len(recs))
			}
			internalLen := 0
			err := b.replayInternal(func(i int, kt keyType, k, v []byte) error {
				want := recs[i]
				if kt != want.kt || !bytes.Equal(k, want.k) || !bytes.Equal(v, want.v) {
					return fmt.Errorf("record %d: got %v %q %q, want %v %q %q", i, kt, k, v, want.kt, want.k, want.v)
				}
				internalLen += len(k) + len(v) + 8
				return nil
			})
			if err != nil {
				t.Fatalf("limit %d %s: %v", limit, name, err)
			}
			if b.internalLen != internalLen {
				t.Fatalf("limit %d %s: got internalLen %d, want %d", limit, name, b.internalLen, internalLen)
			}
		}
		check("built", b)
		loaded := new(Batch)
		if err := loaded.Load(b.Dump()); err != nil {
			t.Fatal(err)
		}
		check("loaded", loaded)
	}
}

func TestBatchAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not stable under the race detector")
	}
	key := []byte("key-0123456789")
	value := bytes.Repeat([]byte("v"), 100)
	b := new(Batch)
	for range 100 {
		b.Put(key, value)
	}
	// A reset batch reuses its buffers.
	if got := testing.AllocsPerRun(100, func() {
		b.Reset()
		for range 50 {
			b.Put(key, value)
			b.Delete(key)
		}
	}); got != 0 {
		t.Errorf("Put/Delete on a reset batch: got %v allocs/op, want 0", got)
	}

	mdb := memdb.New(comparer.DefaultComparer, 1<<20)
	ik, err := b.putMem(1, mdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq := uint64(1)
	if got := testing.AllocsPerRun(100, func() {
		mdb.Reset()
		seq += uint64(b.Len())
		if ik, err = b.putMem(seq, mdb, ik); err != nil {
			t.Fatal(err)
		}
	}); got != 0 {
		t.Errorf("putMem with a scratch key: got %v allocs/op, want 0", got)
	}
}
