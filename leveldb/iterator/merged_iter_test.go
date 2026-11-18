// Copyright (c) 2014, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package iterator_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/wavesplatform/goleveldb/leveldb/comparer"
	"github.com/wavesplatform/goleveldb/leveldb/errors"
	. "github.com/wavesplatform/goleveldb/leveldb/iterator"
	"github.com/wavesplatform/goleveldb/leveldb/storage"
	"github.com/wavesplatform/goleveldb/leveldb/testutil"
)

var _ = testutil.Defer(func() {
	Describe("Merged iterator", func() {
		Test := func(filled int, empty int) func() {
			return func() {
				It("Should iterates and seeks correctly", func(done Done) {
					rnd := testutil.NewRand()

					// Build key/value.
					filledKV := make([]testutil.KeyValue, filled)
					kv := testutil.KeyValue_Generate(nil, 100, 1, 1, 10, 4, 4)
					kv.Iterate(func(i int, key, value []byte) {
						filledKV[rnd.Intn(filled)].Put(key, value)
					})

					// Create itearators.
					iters := make([]Iterator, filled+empty)
					for i := range iters {
						if empty == 0 || (rnd.Int()%2 == 0 && filled > 0) {
							filled--
							Expect(filledKV[filled].Len()).ShouldNot(BeZero())
							iters[i] = NewArrayIterator(filledKV[filled])
						} else {
							empty--
							iters[i] = NewEmptyIterator(nil)
						}
					}

					// Test the iterator.
					t := testutil.IteratorTesting{
						KeyValue: kv.Clone(),
						Iter:     NewMergedIterator(iters, comparer.DefaultComparer, true),
					}
					testutil.DoIteratorTesting(&t)
					done <- true
				}, 15.0)
			}
		}

		Describe("with three, all filled iterators", Test(3, 0))
		Describe("with one filled, one empty iterators", Test(1, 1))
		Describe("with one filled, two empty iterators", Test(1, 2))
	})
})

func BenchmarkMergedIterator(b *testing.B) {
	n := 11
	iters := make([]Iterator, n)
	for i := range iters {
		kv := testutil.KeyValue_Generate(nil, 100, 1, 1, 10, 4, 4)
		iters[i] = NewArrayIterator(kv)
	}

	mi := NewMergedIterator(iters, comparer.DefaultComparer, true)

	for b.Loop() {
		mi.First()
		for mi.Next() {
			mi.Key()
		}
	}
}

func newTestMergedIterator(n int) (Iterator, *testutil.KeyValue) {
	all := &testutil.KeyValue{}
	iters := make([]Iterator, n)
	for i := range iters {
		kv := &testutil.KeyValue{}
		for j := range 50 {
			// Interleave the keys of the input iterators.
			key := fmt.Appendf(nil, "key%05d", j*n+i)
			kv.Put(key, key)
			all.PutU(key, key)
		}
		iters[i] = NewArrayIterator(kv)
	}
	return NewMergedIterator(iters, comparer.DefaultComparer, true), all
}

// TestMergedIteratorAllocs checks that seeking and iterating, in both
// directions and with direction changes, does not allocate.
func TestMergedIteratorAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not stable under the race detector")
	}
	mi, all := newTestMergedIterator(5)
	defer mi.Release()

	check := func(op string, fn func()) {
		t.Helper()
		if got := testing.AllocsPerRun(100, fn); got != 0 {
			t.Errorf("%s: got %v allocs/op, want 0", op, got)
		}
	}
	check("forward", func() {
		for ok := mi.First(); ok; ok = mi.Next() {
		}
	})
	check("reverse", func() {
		for ok := mi.Last(); ok; ok = mi.Prev() {
		}
	})
	i := 0
	check("seek", func() {
		i++
		if !mi.Seek(all.KeyAt(i % all.Len())) {
			t.Fatal("seek failed")
		}
	})
	check("direction change", func() {
		i++
		mi.Seek(all.KeyAt(1 + i%(all.Len()-2)))
		mi.Prev()
		mi.Next()
	})
}

// TestMergedIteratorRandomWalk checks the merged iterator against the sorted
// union of its inputs, with random moves and direction changes.
func TestMergedIteratorRandomWalk(t *testing.T) {
	mi, all := newTestMergedIterator(7)
	defer mi.Release()

	rnd := rand.New(rand.NewSource(1))
	pos := -1 // -1 is before the first key, all.Len() after the last
	for step := range 10000 {
		var ok bool
		switch op := rnd.Intn(10); {
		case op == 0:
			ok, pos = mi.First(), 0
		case op == 1:
			ok, pos = mi.Last(), all.Len()-1
		case op == 2:
			pos = rnd.Intn(all.Len())
			ok = mi.Seek(all.KeyAt(pos))
		case op < 6:
			if pos < all.Len() {
				pos++
			}
			ok = mi.Next()
		default:
			if pos >= 0 {
				pos--
			}
			ok = mi.Prev()
		}
		wantOK := pos >= 0 && pos < all.Len()
		if ok != wantOK {
			t.Fatalf("step %d: got ok=%v, want %v at %d", step, ok, wantOK, pos)
		}
		if ok && !bytes.Equal(mi.Key(), all.KeyAt(pos)) {
			t.Fatalf("step %d: got key %q, want %q", step, mi.Key(), all.KeyAt(pos))
		}
		if ok && !bytes.Equal(mi.Value(), all.KeyAt(pos)) {
			t.Fatalf("step %d: got value %q, want %q", step, mi.Value(), all.KeyAt(pos))
		}
	}
	if err := mi.Error(); err != nil {
		t.Fatal(err)
	}
}

// failingIterator wraps an iterator and, once failing is set, fails every
// move with err.
type failingIterator struct {
	Iterator
	err     error
	failing bool
}

func (i *failingIterator) First() bool          { return !i.failing && i.Iterator.First() }
func (i *failingIterator) Last() bool           { return !i.failing && i.Iterator.Last() }
func (i *failingIterator) Seek(key []byte) bool { return !i.failing && i.Iterator.Seek(key) }
func (i *failingIterator) Next() bool           { return !i.failing && i.Iterator.Next() }
func (i *failingIterator) Prev() bool           { return !i.failing && i.Iterator.Prev() }

func (i *failingIterator) Error() error {
	if i.failing {
		return i.err
	}
	return i.Iterator.Error()
}

// TestMergedIteratorDirectionChangeError checks that an input iterator
// failing on a direction change stops a strict merged iterator with its
// error, and is skipped by a non-strict one when corrupted, which then
// continues over the healthy inputs.
func TestMergedIteratorDirectionChangeError(t *testing.T) {
	const n, perIter = 4, 50
	for _, strict := range []bool{true, false} {
		// The failing input is the current one or another one.
		for _, failCurrent := range []bool{true, false} {
			// Change to backward with Prev, or to forward with Next.
			for _, backward := range []bool{true, false} {
				if !strict && failCurrent && !backward {
					// Next seeks to the current key and steps past it, so it
					// skips a key when the input holding the current key is
					// dropped. This predates the heap rewrite.
					continue
				}
				name := fmt.Sprintf("strict=%t/current=%t/backward=%t", strict, failCurrent, backward)
				t.Run(name, func(t *testing.T) {
					err := errors.New("input error")
					if !strict {
						err = errors.NewErrCorrupted(storage.FileDesc{}, errors.New("corrupted input"))
					}
					healthy := &testutil.KeyValue{}
					iters := make([]Iterator, n)
					var faulty *failingIterator
					for x := range iters {
						kv := &testutil.KeyValue{}
						for j := range perIter {
							key := fmt.Appendf(nil, "key%05d", j*n+x)
							kv.Put(key, key)
							if x != 0 {
								healthy.PutU(key, key)
							}
						}
						iters[x] = NewArrayIterator(kv)
						if x == 0 {
							faulty = &failingIterator{Iterator: iters[x], err: err}
							iters[x] = faulty
						}
					}
					mi := NewMergedIterator(iters, comparer.DefaultComparer, strict)
					defer mi.Release()

					// Position at a key of the faulty input, or of the next
					// input, moving in the direction opposite to the change.
					j := 20 * n
					if !failCurrent {
						j++
					}
					cur := fmt.Appendf(nil, "key%05d", j)
					ok := mi.Seek(cur)
					if !backward {
						ok = ok && mi.Next() && mi.Prev()
					}
					if !ok || !bytes.Equal(mi.Key(), cur) {
						t.Fatalf("positioned at %q, want %q", mi.Key(), cur)
					}

					faulty.failing = true
					var pos int
					if backward {
						ok = mi.Prev()
						pos = healthy.Search(cur) - 1
					} else {
						ok = mi.Next()
						pos = healthy.Search(cur) + 1
					}
					if strict {
						if ok || mi.Valid() || mi.Key() != nil || mi.Error() != err {
							t.Fatalf("got ok=%t valid=%t key=%q error=%v, want error %v", ok, mi.Valid(), mi.Key(), mi.Error(), err)
						}
						if mi.Next() || mi.Prev() || mi.First() {
							t.Fatal("moved after error")
						}
						return
					}

					// The non-strict iterator now walks the healthy inputs.
					if mi.Error() != nil {
						t.Fatal(mi.Error())
					}
					if !ok || !bytes.Equal(mi.Key(), healthy.KeyAt(pos)) {
						t.Fatalf("got ok=%t key %q, want %q", ok, mi.Key(), healthy.KeyAt(pos))
					}
					rnd := rand.New(rand.NewSource(2))
					for step := range 1000 {
						if rnd.Intn(2) == 0 {
							ok = mi.Next()
							pos = min(pos+1, healthy.Len())
						} else {
							ok = mi.Prev()
							pos = max(pos-1, -1)
						}
						wantOK := pos >= 0 && pos < healthy.Len()
						if ok != wantOK {
							t.Fatalf("step %d: got ok=%t, want %t at %d", step, ok, wantOK, pos)
						}
						if ok && !bytes.Equal(mi.Key(), healthy.KeyAt(pos)) {
							t.Fatalf("step %d: got key %q, want %q", step, mi.Key(), healthy.KeyAt(pos))
						}
					}
					if mi.Error() != nil {
						t.Fatal(mi.Error())
					}
				})
			}
		}
	}
}
