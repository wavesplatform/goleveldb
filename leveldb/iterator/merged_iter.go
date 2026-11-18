// Copyright (c) 2012, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package iterator

import (
	"github.com/wavesplatform/goleveldb/leveldb/comparer"
	"github.com/wavesplatform/goleveldb/leveldb/errors"
	"github.com/wavesplatform/goleveldb/leveldb/util"
)

type dir int

const (
	dirReleased dir = iota - 1
	dirSOI
	dirEOI
	dirBackward
	dirForward
)

type mergedIterator struct {
	cmp    comparer.Comparer
	iters  []Iterator
	strict bool

	keys     [][]byte
	index    int
	dir      dir
	err      error
	errf     func(err error)
	releaser util.Releaser

	// The heap of iterator indexes. While positioned, its root is the
	// current iterator.
	indexes []int
	reverse bool   // if true, indexes is a max-heap
	dirKey  []byte // scratch buffer for the key on a direction change
}

func assertKey(key []byte) []byte {
	if key == nil {
		panic("leveldb/iterator: nil key")
	}
	return key
}

func (i *mergedIterator) iterErr(iter Iterator) bool {
	if err := iter.Error(); err != nil {
		if i.errf != nil {
			i.errf(err)
		}
		if i.strict || !errors.IsCorrupted(err) {
			i.err = err
			return true
		}
	}
	return false
}

func (i *mergedIterator) Valid() bool {
	return i.err == nil && i.dir > dirEOI
}

func (i *mergedIterator) First() bool {
	if i.err != nil {
		return false
	} else if i.dir == dirReleased {
		i.err = ErrIterReleased
		return false
	}

	i.heapReset(false)
	for x, iter := range i.iters {
		switch {
		case iter.First():
			i.keys[x] = assertKey(iter.Key())
			i.indexes = append(i.indexes, x)
		case i.iterErr(iter):
			return false
		default:
			i.keys[x] = nil
		}
	}
	i.heapInit()
	i.dir = dirSOI
	return i.next()
}

func (i *mergedIterator) Last() bool {
	if i.err != nil {
		return false
	} else if i.dir == dirReleased {
		i.err = ErrIterReleased
		return false
	}

	i.heapReset(true)
	for x, iter := range i.iters {
		switch {
		case iter.Last():
			i.keys[x] = assertKey(iter.Key())
			i.indexes = append(i.indexes, x)
		case i.iterErr(iter):
			return false
		default:
			i.keys[x] = nil
		}
	}
	i.heapInit()
	i.dir = dirEOI
	return i.prev()
}

func (i *mergedIterator) Seek(key []byte) bool {
	if i.err != nil {
		return false
	} else if i.dir == dirReleased {
		i.err = ErrIterReleased
		return false
	}

	i.heapReset(false)
	for x, iter := range i.iters {
		switch {
		case iter.Seek(key):
			i.keys[x] = assertKey(iter.Key())
			i.indexes = append(i.indexes, x)
		case i.iterErr(iter):
			return false
		default:
			i.keys[x] = nil
		}
	}
	i.heapInit()
	i.dir = dirSOI
	return i.next()
}

func (i *mergedIterator) next() bool {
	if len(i.indexes) == 0 {
		i.dir = dirEOI
		return false
	}
	i.index = i.indexes[0]
	i.dir = dirForward
	return true
}

func (i *mergedIterator) Next() bool {
	if i.dir == dirEOI || i.err != nil {
		return false
	} else if i.dir == dirReleased {
		i.err = ErrIterReleased
		return false
	}

	switch i.dir {
	case dirSOI:
		return i.First()
	case dirBackward:
		i.dirKey = append(i.dirKey[:0], i.keys[i.index]...)
		if !i.Seek(i.dirKey) {
			return false
		}
		return i.Next()
	}

	if !i.fixRoot(i.iters[i.index].Next()) {
		return false
	}
	return i.next()
}

func (i *mergedIterator) prev() bool {
	if len(i.indexes) == 0 {
		i.dir = dirSOI
		return false
	}
	i.index = i.indexes[0]
	i.dir = dirBackward
	return true
}

func (i *mergedIterator) Prev() bool {
	if i.dir == dirSOI || i.err != nil {
		return false
	} else if i.dir == dirReleased {
		i.err = ErrIterReleased
		return false
	}

	switch i.dir {
	case dirEOI:
		return i.Last()
	case dirForward:
		i.dirKey = append(i.dirKey[:0], i.keys[i.index]...)
		key := i.dirKey
		i.heapReset(true)
		for x, iter := range i.iters {
			if x == i.index {
				continue
			}
			seek := iter.Seek(key)
			switch {
			case seek && iter.Prev(), !seek && iter.Last():
				i.keys[x] = assertKey(iter.Key())
				i.indexes = append(i.indexes, x)
			case i.iterErr(iter):
				return false
			default:
				i.keys[x] = nil
			}
		}
		i.heapInit()

		// The current iterator is not in the heap, it is pushed once moved.
		x := i.index
		iter := i.iters[x]
		switch {
		case iter.Prev():
			i.keys[x] = assertKey(iter.Key())
			i.heapPush(x)
		case i.iterErr(iter):
			return false
		default:
			i.keys[x] = nil
		}
		return i.prev()
	}

	if !i.fixRoot(i.iters[i.index].Prev()) {
		return false
	}
	return i.prev()
}

func (i *mergedIterator) Key() []byte {
	if i.err != nil || i.dir <= dirEOI {
		return nil
	}
	return i.keys[i.index]
}

func (i *mergedIterator) Value() []byte {
	if i.err != nil || i.dir <= dirEOI {
		return nil
	}
	return i.iters[i.index].Value()
}

func (i *mergedIterator) Release() {
	if i.dir != dirReleased {
		i.dir = dirReleased
		for _, iter := range i.iters {
			iter.Release()
		}
		i.iters = nil
		i.keys = nil
		i.indexes = nil
		if i.releaser != nil {
			i.releaser.Release()
			i.releaser = nil
		}
	}
}

func (i *mergedIterator) SetReleaser(releaser util.Releaser) {
	if i.dir == dirReleased {
		panic(util.ErrReleased)
	}
	if i.releaser != nil && releaser != nil {
		panic(util.ErrHasReleaser)
	}
	i.releaser = releaser
}

func (i *mergedIterator) Error() error {
	return i.err
}

func (i *mergedIterator) SetErrorCallback(f func(err error)) {
	i.errf = f
}

// NewMergedIterator returns an iterator that merges its input. Walking the
// resultant iterator will return all key/value pairs of all input iterators
// in strictly increasing key order, as defined by cmp.
// The input's key ranges may overlap, but there are assumed to be no duplicate
// keys: if iters[i] contains a key k then iters[j] will not contain that key k.
// None of the iters may be nil.
//
// If strict is true the any 'corruption errors' (i.e errors.IsCorrupted(err) == true)
// won't be ignored and will halt 'merged iterator', otherwise the iterator will
// continue to the next 'input iterator'.
func NewMergedIterator(iters []Iterator, cmp comparer.Comparer, strict bool) Iterator {
	return &mergedIterator{
		iters:   iters,
		cmp:     cmp,
		strict:  strict,
		keys:    make([][]byte, len(iters)),
		indexes: make([]int, 0, len(iters)),
	}
}

// fixRoot updates the heap after its root, the current iterator, moved; ok
// is the result of the move. It returns false if the move failed with an
// error that halts the iterator.
func (i *mergedIterator) fixRoot(ok bool) bool {
	x := i.indexes[0]
	iter := i.iters[x]
	switch {
	case ok:
		i.keys[x] = assertKey(iter.Key())
	case i.iterErr(iter):
		return false
	default:
		i.keys[x] = nil
		n := len(i.indexes) - 1
		i.indexes[0] = i.indexes[n]
		i.indexes = i.indexes[:n]
	}
	i.heapDown(0)
	return true
}

// The heap operations below follow container/heap, without its interface
// dispatch.

func (i *mergedIterator) heapLess(a, b int) bool {
	r := i.cmp.Compare(i.keys[a], i.keys[b])
	if i.reverse {
		return r > 0
	}
	return r < 0
}

func (i *mergedIterator) heapReset(reverse bool) {
	i.reverse = reverse
	i.indexes = i.indexes[:0]
}

func (i *mergedIterator) heapInit() {
	for j := len(i.indexes)/2 - 1; j >= 0; j-- {
		i.heapDown(j)
	}
}

func (i *mergedIterator) heapPush(x int) {
	i.indexes = append(i.indexes, x)
	h := i.indexes
	j := len(h) - 1
	for j > 0 {
		p := (j - 1) / 2
		if !i.heapLess(h[j], h[p]) {
			break
		}
		h[p], h[j] = h[j], h[p]
		j = p
	}
}

func (i *mergedIterator) heapDown(j int) {
	h := i.indexes
	n := len(h)
	for {
		c := 2*j + 1
		if c >= n {
			break
		}
		if r := c + 1; r < n && i.heapLess(h[r], h[c]) {
			c = r
		}
		if !i.heapLess(h[c], h[j]) {
			break
		}
		h[j], h[c] = h[c], h[j]
		j = c
	}
}
