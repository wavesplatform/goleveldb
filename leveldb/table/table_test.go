// Copyright (c) 2014, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package table

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/wavesplatform/goleveldb/leveldb/cache"
	"github.com/wavesplatform/goleveldb/leveldb/errors"
	"github.com/wavesplatform/goleveldb/leveldb/filter"
	"github.com/wavesplatform/goleveldb/leveldb/iterator"
	"github.com/wavesplatform/goleveldb/leveldb/opt"
	"github.com/wavesplatform/goleveldb/leveldb/storage"
	"github.com/wavesplatform/goleveldb/leveldb/testutil"
	"github.com/wavesplatform/goleveldb/leveldb/util"
)

type tableWrapper struct {
	r *Reader
}

func (t tableWrapper) TestFind(key []byte) (rkey, rvalue []byte, err error) {
	return t.r.Find(key, false, nil)
}

func (t tableWrapper) TestGet(key []byte) (value []byte, err error) {
	return t.r.Get(key, nil)
}

func (t tableWrapper) TestNewIterator(slice *util.Range) iterator.Iterator {
	return t.r.NewIterator(slice, nil)
}

var _ = testutil.Defer(func() {
	Describe("Table", func() {
		Describe("approximate offset test", func() {
			var (
				buf = &bytes.Buffer{}
				o   = &opt.Options{
					BlockSize:   1024,
					Compression: opt.NoCompression,
				}
			)

			// Building the table.
			tw := NewWriter(buf, o, nil, 0)
			err := tw.Append([]byte("k01"), []byte("hello"))
			Expect(err).ShouldNot(HaveOccurred())
			err = tw.Append([]byte("k02"), []byte("hello2"))
			Expect(err).ShouldNot(HaveOccurred())
			err = tw.Append([]byte("k03"), bytes.Repeat([]byte{'x'}, 10000))
			Expect(err).ShouldNot(HaveOccurred())
			err = tw.Append([]byte("k04"), bytes.Repeat([]byte{'x'}, 200000))
			Expect(err).ShouldNot(HaveOccurred())
			err = tw.Append([]byte("k05"), bytes.Repeat([]byte{'x'}, 300000))
			Expect(err).ShouldNot(HaveOccurred())
			err = tw.Append([]byte("k06"), []byte("hello3"))
			Expect(err).ShouldNot(HaveOccurred())
			err = tw.Append([]byte("k07"), bytes.Repeat([]byte{'x'}, 100000))
			Expect(err).ShouldNot(HaveOccurred())

			err = tw.Close()

			It("Should be able to approximate offset of a key correctly", func() {
				Expect(err).ShouldNot(HaveOccurred())

				tr, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), storage.FileDesc{}, nil, nil, o)
				Expect(err).ShouldNot(HaveOccurred())
				CheckOffset := func(key string, expect, threshold int) {
					offset, err := tr.OffsetOf([]byte(key))
					Expect(err).ShouldNot(HaveOccurred())
					Expect(offset).Should(BeNumerically("~", expect, threshold), "Offset of key %q", key)
				}

				CheckOffset("k0", 0, 0)
				CheckOffset("k01a", 0, 0)
				CheckOffset("k02", 0, 0)
				CheckOffset("k03", 0, 0)
				CheckOffset("k04", 10000, 1000)
				CheckOffset("k04a", 210000, 1000)
				CheckOffset("k05", 210000, 1000)
				CheckOffset("k06", 510000, 1000)
				CheckOffset("k07", 510000, 1000)
				CheckOffset("xyz", 610000, 2000)
			})
		})

		Describe("read test", func() {
			Build := func(kv testutil.KeyValue) testutil.DB {
				o := &opt.Options{
					BlockSize:            512,
					BlockRestartInterval: 3,
				}
				buf := &bytes.Buffer{}

				// Building the table.
				tw := NewWriter(buf, o, nil, 0)
				kv.Iterate(func(i int, key, value []byte) {
					Expect(tw.Append(key, value)).ShouldNot(HaveOccurred())
				})
				tw.Close()

				// Opening the table.
				tr, _ := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), storage.FileDesc{}, nil, nil, o)
				return tableWrapper{tr}
			}
			Test := func(kv *testutil.KeyValue, body func(r *Reader)) func() {
				return func() {
					db := Build(*kv)
					if body != nil {
						body(db.(tableWrapper).r)
					}
					testutil.KeyValueTesting(nil, *kv, db, nil, nil)
				}
			}

			testutil.AllKeyValueTesting(nil, Build, nil, nil)
			Describe("with one key per block", Test(testutil.KeyValue_Generate(nil, 9, 1, 1, 10, 512, 512), func(r *Reader) {
				It("should have correct blocks number", func() {
					indexBlock, err := r.readBlock(r.indexBH, true)
					Expect(err).To(BeNil())
					Expect(indexBlock.restartsLen).Should(Equal(9))
				})
			}))
		})
	})
})

func TestMinLZRoundTrip(t *testing.T) {
	const mib = 1 << 20
	// generate random data
	r := rand.NewChaCha8([32]byte{1, 2, 3, 4})
	data := make([]byte, 10*mib)
	if _, err := io.ReadFull(r, data[mib:]); err != nil { // leave first MB zeroed
		t.Fatalf("failed to generate random data: %v", err)
	}
	// compress with min lz
	compressed, err := minLZEncodeTo(nil, data)
	if err != nil {
		t.Fatalf("failed to compress data: %v", err)
	}
	// decompress
	decompressed, err := minLZDecodeTo(nil, compressed)
	if err != nil {
		t.Fatalf("failed to decompress data: %v", err)
	}
	// compare
	if !bytes.Equal(data, decompressed) {
		t.Fatalf("decompressed data does not match original")
	}
}

// TestReaderLookupAllocs locks in the allocation counts of point lookups.
// With a block cache the remaining allocations are the cache handles of the
// index and data blocks and the private copy of the value, or of the key and
// value together for Find. Without one they are the decoded block buffer and
// the private copies.
func TestReaderLookupAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not stable under the race detector")
	}
	for _, tc := range []struct {
		name                     string
		cached                   bool
		get, getMissing, findMax float64
		offsetOf                 float64
	}{
		{name: "cached", cached: true, get: 3, getMissing: 2, findMax: 3, offsetOf: 1},
		{name: "uncached", cached: false, get: 2, getMissing: 2, findMax: 3, offsetOf: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bt := openBenchTable(t, opt.SnappyCompression, true, tc.cached)
			// Fill the block cache.
			for _, key := range bt.keys {
				if _, err := bt.r.Get(key, nil); err != nil {
					t.Fatal("get: ", err)
				}
			}
			i := 0
			check := func(op string, limit float64, fn func()) {
				t.Helper()
				if n := testing.AllocsPerRun(1000, fn); n > limit {
					t.Errorf("%s: got %v allocs/op, want <= %v", op, n, limit)
				}
			}
			check("Get", tc.get, func() {
				i++
				if _, err := bt.r.Get(bt.key(i), nil); err != nil {
					t.Fatal("get: ", err)
				}
			})
			check("Get missing", tc.getMissing, func() {
				i++
				if _, err := bt.r.Get(bt.missing[bt.order[i%len(bt.order)]], nil); err != ErrNotFound {
					t.Fatal("get missing: ", err)
				}
			})
			check("Find", tc.findMax, func() {
				i++
				if _, _, err := bt.r.Find(bt.key(i), false, nil); err != nil {
					t.Fatal("find: ", err)
				}
			})
			check("OffsetOf", tc.offsetOf, func() {
				i++
				if _, err := bt.r.OffsetOf(bt.key(i)); err != nil {
					t.Fatal("offset of: ", err)
				}
			})
		})
	}
}

// countDataBlocks returns the number of data blocks of the table.
func countDataBlocks(t *testing.T, r *Reader) int {
	t.Helper()
	b, rel, err := r.getIndexBlock(true)
	if err != nil {
		t.Fatal("index block: ", err)
	}
	defer rel.Release()
	n := 0
	for it := r.newBlockIter(b, nil, nil, true); it.Next(); {
		n++
	}
	return n
}

// TestReaderIterAllocs checks that table iterators recycle their data block
// iterators: with a block cache, seeking and scanning only allocate a cache
// handle per data block entered.
func TestReaderIterAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not stable under the race detector")
	}
	bt := openBenchTable(t, opt.SnappyCompression, true, true)
	blocks := float64(countDataBlocks(t, bt.r))
	if blocks < 2 {
		t.Fatalf("got %v data blocks, want several", blocks)
	}
	iter := bt.r.NewIterator(nil, nil)
	defer iter.Release()

	check := func(op string, limit float64, fn func()) {
		t.Helper()
		if got := testing.AllocsPerRun(20, fn); got > limit {
			t.Errorf("%s: got %v allocs/op, want <= %v", op, got, limit)
		}
	}
	n := 0
	check("forward scan", blocks, func() {
		n = 0
		for ok := iter.First(); ok; ok = iter.Next() {
			n++
		}
	})
	if n != len(bt.keys) {
		t.Fatalf("forward scan: got %d keys, want %d", n, len(bt.keys))
	}
	check("reverse scan", blocks, func() {
		n = 0
		for ok := iter.Last(); ok; ok = iter.Prev() {
			n++
		}
	})
	if n != len(bt.keys) {
		t.Fatalf("reverse scan: got %d keys, want %d", n, len(bt.keys))
	}
	i := 0
	check("seek", 1, func() {
		i++
		if !iter.Seek(bt.key(i)) {
			t.Fatal("seek: key not found")
		}
	})
}

// TestReaderIterRecycle walks a table iterator back and forth across data
// block boundaries, where data block iterators are recycled, and checks the
// keys and values against the table contents.
func TestReaderIterRecycle(t *testing.T) {
	for _, cached := range []bool{true, false} {
		t.Run(fmt.Sprintf("cached=%v", cached), func(t *testing.T) {
			bt := openBenchTable(t, opt.SnappyCompression, true, cached)
			for _, slice := range []*util.Range{nil, {Start: bt.keys[100], Limit: bt.keys[len(bt.keys)-100]}} {
				first, last := 0, len(bt.keys)-1
				if slice != nil {
					first, last = 100, len(bt.keys)-101
				}
				iter := bt.r.NewIterator(slice, nil)
				rnd := rand.New(rand.NewPCG(3, 5))
				pos := first - 1
				for step := range 20000 {
					var ok bool
					switch op := rnd.IntN(100); {
					case op == 0:
						ok, pos = iter.First(), first
					case op == 1:
						ok, pos = iter.Last(), last
					case op == 2:
						pos = first + rnd.IntN(last-first+1)
						ok = iter.Seek(bt.keys[pos])
					case op < 51:
						if pos <= last {
							pos++
						}
						ok = iter.Next()
					default:
						if pos >= first {
							pos--
						}
						ok = iter.Prev()
					}
					wantOK := pos >= first && pos <= last
					if ok != wantOK {
						t.Fatalf("step %d: got ok=%v, want %v at %d", step, ok, wantOK, pos)
					}
					if ok && (!bytes.Equal(iter.Key(), bt.keys[pos]) || !bytes.Equal(iter.Value(), bt.values[pos])) {
						t.Fatalf("step %d: got key %q, want %q", step, iter.Key(), bt.keys[pos])
					}
				}
				if err := iter.Error(); err != nil {
					t.Fatal(err)
				}
				iter.Release()
			}
		})
	}
}

// openLookupTable builds a table with keys of very different lengths, some
// longer than the key buffers kept by pooled lookup state, and opens it in
// the given cache and buffer pool configuration.
func openLookupTable(t *testing.T, withCache, withPool bool) (r *Reader, keys, values [][]byte) {
	t.Helper()
	rnd := rand.New(rand.NewPCG(7, 11))
	for i := range 1000 {
		var n int
		switch i % 4 {
		case 0:
			n = 6 + rnd.IntN(10)
		case 1:
			n = 100 + rnd.IntN(200)
		case 2:
			n = 16
		default:
			n = findStateMaxKeyBuf + rnd.IntN(1024)
		}
		key := make([]byte, n)
		copy(key, fmt.Appendf(nil, "%06d", i))
		for j := 6; j < n; j++ {
			key[j] = byte('a' + rnd.IntN(26))
		}
		keys = append(keys, key)
		values = append(values, fmt.Appendf(nil, "value-%d-%s", i, key[:min(n, 32)]))
	}

	o := &opt.Options{BlockSize: 1024, BlockRestartInterval: 4, Compression: opt.SnappyCompression, Filter: filter.NewBloomFilter(10)}
	buf := new(bytes.Buffer)
	buildBenchTable(t, buf, o, nil, keys, values)

	var (
		ns    *cache.NamespaceGetter
		bpool *util.BufferPool
	)
	if withCache {
		ns = &cache.NamespaceGetter{Cache: cache.NewCache(cache.NewLRU(8 * opt.MiB))}
	}
	if withPool {
		bpool = util.NewBufferPool(o.GetBlockSize() + 5)
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), storage.FileDesc{}, ns, bpool, o)
	if err != nil {
		t.Fatal("new reader: ", err)
	}
	t.Cleanup(r.Release)
	return r, keys, values
}

// TestReaderLookupResults checks that point lookups return correct and
// private results with pooled lookup state, for every cache and buffer pool
// configuration, also when run concurrently.
func TestReaderLookupResults(t *testing.T) {
	for _, withCache := range []bool{false, true} {
		for _, withPool := range []bool{false, true} {
			t.Run(fmt.Sprintf("cache=%t/pool=%t", withCache, withPool), func(t *testing.T) {
				r, keys, values := openLookupTable(t, withCache, withPool)
				check := func(i int) error {
					key := keys[i]
					value, err := r.Get(key, nil)
					if err != nil {
						return fmt.Errorf("get %d: %w", i, err)
					}
					if !bytes.Equal(value, values[i]) {
						return fmt.Errorf("get %d: got value %q, want %q", i, value, values[i])
					}
					// The caller owns the value.
					clear(value)

					rkey, rvalue, err := r.Find(key, true, nil)
					if err != nil {
						return fmt.Errorf("find %d: %w", i, err)
					}
					if !bytes.Equal(rkey, key) || !bytes.Equal(rvalue, values[i]) {
						return fmt.Errorf("find %d: got %q/%q", i, rkey, rvalue)
					}
					// A later lookup must not overwrite the returned key,
					// even though the lookup state is reused.
					next := keys[(i+1)%len(keys)]
					if _, err := r.FindKey(next, false, nil); err != nil {
						return fmt.Errorf("find key %d: %w", i+1, err)
					}
					if !bytes.Equal(rkey, key) {
						return fmt.Errorf("find %d: returned key changed to %q", i, rkey)
					}
					// The key and value may share one allocation; appending
					// to the key must not overwrite the value.
					_ = append(rkey, bytes.Repeat([]byte{'x'}, len(rvalue)+1)...)
					if !bytes.Equal(rvalue, values[i]) {
						return fmt.Errorf("find %d: appending to the key changed the value to %q", i, rvalue)
					}
					clear(rkey)
					clear(rvalue)

					// A missing key sorts right after keys[i]: Get must miss
					// and Find must return the next key.
					missing := append(append([]byte(nil), key...), 0)
					if _, err := r.Get(missing, nil); err != ErrNotFound {
						return fmt.Errorf("get missing %d: got %v, want ErrNotFound", i, err)
					}
					rkey, rvalue, err = r.Find(missing, false, nil)
					if i == len(keys)-1 {
						if err != ErrNotFound {
							return fmt.Errorf("find after last: got %v, want ErrNotFound", err)
						}
					} else if err != nil || !bytes.Equal(rkey, keys[i+1]) || !bytes.Equal(rvalue, values[i+1]) {
						return fmt.Errorf("find missing %d: got %q/%q/%v", i, rkey, rvalue, err)
					}

					if _, err := r.OffsetOf(key); err != nil {
						return fmt.Errorf("offset of %d: %w", i, err)
					}
					return nil
				}

				for i := range keys {
					if err := check(i); err != nil {
						t.Fatal(err)
					}
				}

				// The returned values were cleared above, a second pass
				// sees the table data unchanged.
				var wg sync.WaitGroup
				errs := make(chan error, 4)
				for g := range 4 {
					wg.Go(func() {
						for i := g; i < len(keys); i += 4 {
							if err := check(i); err != nil {
								errs <- err
								return
							}
						}
					})
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					t.Error(err)
				}
			})
		}
	}
}

// Point lookups and iteration over a table with a corrupted data block
// return corruption errors for that block, and leave the pooled lookup state
// and recycled block iterators usable for lookups in the healthy blocks.
func TestReaderCorruptedDataBlock(t *testing.T) {
	var keys, values [][]byte
	for i := range 400 {
		keys = append(keys, fmt.Appendf(nil, "%06d", i))
		values = append(values, fmt.Appendf(nil, "value-%d", i))
	}
	o := &opt.Options{
		BlockSize:   256,
		Compression: opt.NoCompression,
		Strict:      opt.StrictBlockChecksum,
	}
	buf := new(bytes.Buffer)
	buildBenchTable(t, buf, o, nil, keys, values)
	data := bytes.Clone(buf.Bytes())
	// Corrupt a data block in the middle of the data blocks.
	data[600] ^= 0xff

	for _, withCache := range []bool{false, true} {
		for _, withPool := range []bool{false, true} {
			t.Run(fmt.Sprintf("cache=%t/pool=%t", withCache, withPool), func(t *testing.T) {
				var (
					ns    *cache.NamespaceGetter
					bpool *util.BufferPool
				)
				if withCache {
					ns = &cache.NamespaceGetter{Cache: cache.NewCache(cache.NewLRU(opt.MiB))}
				}
				if withPool {
					bpool = util.NewBufferPool(o.GetBlockSize() + 5)
				}
				r, err := NewReader(bytes.NewReader(data), int64(len(data)), storage.FileDesc{}, ns, bpool, o)
				if err != nil {
					t.Fatal("new reader: ", err)
				}
				defer r.Release()

				lookup := func() (corrupted []bool) {
					for i, key := range keys {
						value, err := r.Get(key, nil)
						switch {
						case errors.IsCorrupted(err):
							corrupted = append(corrupted, true)
							continue
						case err != nil:
							t.Fatalf("get %d: %v", i, err)
						case !bytes.Equal(value, values[i]):
							t.Fatalf("get %d: got value %q, want %q", i, value, values[i])
						}
						corrupted = append(corrupted, false)

						// A missing key sorts after keys[i], so Find may
						// have to read the next, possibly corrupted, block.
						missing := append(bytes.Clone(key), 0)
						rkey, _, err := r.Find(missing, false, nil)
						switch {
						case i == len(keys)-1:
							if err != ErrNotFound {
								t.Fatalf("find after last: got %v, want ErrNotFound", err)
							}
						case errors.IsCorrupted(err):
						case err != nil:
							t.Fatalf("find missing %d: %v", i, err)
						case !bytes.Equal(rkey, keys[i+1]):
							t.Fatalf("find missing %d: got key %q, want %q", i, rkey, keys[i+1])
						}
					}
					return corrupted
				}

				first := lookup()
				var nCorrupted int
				for _, c := range first {
					if c {
						nCorrupted++
					}
				}
				if nCorrupted == 0 || nCorrupted == len(keys) {
					t.Fatalf("got %d of %d keys in corrupted blocks, want some", nCorrupted, len(keys))
				}
				if second := lookup(); fmt.Sprint(second) != fmt.Sprint(first) {
					t.Fatal("repeated lookups gave different results")
				}

				// A strict iteration stops with a corruption error at the
				// corrupted block, a non-strict one skips it. The recycled
				// block iterators then serve a healthy iteration.
				strict := &opt.ReadOptions{Strict: opt.StrictReader}
				for range 2 {
					iter := r.NewIterator(nil, strict)
					n := 0
					for iter.Next() {
						if !bytes.Equal(iter.Key(), keys[n]) {
							t.Fatalf("strict iter %d: got key %q, want %q", n, iter.Key(), keys[n])
						}
						n++
					}
					if !errors.IsCorrupted(iter.Error()) {
						t.Fatalf("strict iter: got error %v, want corruption", iter.Error())
					}
					iter.Release()

					iter = r.NewIterator(nil, nil)
					var got [][]byte
					for iter.Next() {
						got = append(got, bytes.Clone(iter.Key()))
					}
					if err := iter.Error(); err != nil {
						t.Fatal("iter: ", err)
					}
					iter.Release()
					var want [][]byte
					for i, c := range first {
						if !c {
							want = append(want, keys[i])
						}
					}
					if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
						t.Fatalf("iter: got %d keys, want the %d keys outside the corrupted block", len(got), len(want))
					}
				}
			})
		}
	}
}
