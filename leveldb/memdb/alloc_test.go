package memdb

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/wavesplatform/goleveldb/leveldb/comparer"
)

func TestPutAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not stable under the race detector")
	}
	const n = 1000
	p := New(comparer.DefaultComparer, 1<<20)
	key := make([]byte, 8)
	value := make([]byte, 100)
	fill := func() {
		for i := range n {
			binary.BigEndian.PutUint64(key, uint64(i))
			if err := p.Put(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	fill()
	// After Reset the node and KV buffers are reused, so refilling the DB
	// to the same size allocates nothing.
	p.Reset()
	i := 0
	if got := testing.AllocsPerRun(n-1, func() {
		binary.BigEndian.PutUint64(key, uint64(i))
		i++
		if err := p.Put(key, value); err != nil {
			t.Fatal(err)
		}
	}); got != 0 {
		t.Errorf("Put after Reset: got %v allocs/op, want 0", got)
	}
	// Overwriting an existing key with a value of the same size.
	if got := testing.AllocsPerRun(100, func() {
		binary.BigEndian.PutUint64(key, 7)
		if err := p.Put(key, nil); err != nil {
			t.Fatal(err)
		}
	}); got != 0 {
		t.Errorf("overwrite: got %v allocs/op, want 0", got)
	}
}

func TestGrowNodes(t *testing.T) {
	const capacity = 1 << 20
	key := make([]byte, 8)
	value := make([]byte, 100)

	// A mostly empty DB doesn't preallocate node data for the whole
	// capacity.
	p := New(comparer.DefaultComparer, capacity)
	for i := range 10 {
		binary.BigEndian.PutUint64(key, uint64(i))
		if err := p.Put(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if c := cap(p.nodeData); c > 1024 {
		t.Errorf("10 entries: node data capacity %d, want <= 1024", c)
	}

	// Filling the DB up to its capacity.
	n := 0
	for p.Free() >= len(key)+len(value) {
		binary.BigEndian.PutUint64(key, uint64(n))
		n++
		if err := p.Put(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if l, c := len(p.nodeData), cap(p.nodeData); c > 2*l {
		t.Errorf("full: node data capacity %d for length %d", c, l)
	}
	if p.Len() != n {
		t.Fatalf("got %d entries, want %d", p.Len(), n)
	}
	iter := p.NewIterator(nil)
	defer iter.Release()
	i := 0
	for iter.Next() {
		binary.BigEndian.PutUint64(key, uint64(i))
		if !bytes.Equal(iter.Key(), key) || !bytes.Equal(iter.Value(), value) {
			t.Fatalf("entry %d: got key %x", i, iter.Key())
		}
		i++
	}
	if i != n {
		t.Fatalf("iterated %d entries, want %d", i, n)
	}
}

// TestResetHeights checks that a reset DB builds the same skiplist as a new
// one, and that Reset doesn't allocate.
func TestResetHeights(t *testing.T) {
	key := make([]byte, 8)
	fill := func(p *DB) {
		for i := range 500 {
			binary.BigEndian.PutUint64(key, uint64(i))
			if err := p.Put(key, key); err != nil {
				t.Fatal(err)
			}
		}
	}
	fresh := New(comparer.DefaultComparer, 0)
	fill(fresh)
	reused := New(comparer.DefaultComparer, 0)
	for range 3 {
		fill(reused)
		reused.Reset()
	}
	fill(reused)
	if !slices.Equal(fresh.nodeData, reused.nodeData) || fresh.maxHeight != reused.maxHeight {
		t.Fatal("reset DB differs from a new DB")
	}
	if !raceEnabled {
		if got := testing.AllocsPerRun(10, reused.Reset); got != 0 {
			t.Errorf("Reset: got %v allocs/op, want 0", got)
		}
	}
}
