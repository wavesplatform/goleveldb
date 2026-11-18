// Copyright (c) 2012, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package leveldb

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/wavesplatform/goleveldb/leveldb/iterator"
	"github.com/wavesplatform/goleveldb/leveldb/opt"
	"github.com/wavesplatform/goleveldb/leveldb/util"
)

type snapshotElement struct {
	seq        uint64
	ref        int
	prev, next *snapshotElement
}

// snapshotList is an intrusive doubly linked list of snapshot elements,
// ordered by sequence number. The zero value is an empty list. It keeps one
// released element for reuse, so that the short-lived snapshots taken by
// DB.Get and DB.Has don't allocate.
type snapshotList struct {
	front, back *snapshotElement
	free        *snapshotElement
}

func (l *snapshotList) pushBack(seq uint64) *snapshotElement {
	se := l.free
	if se != nil {
		l.free = nil
		*se = snapshotElement{}
	} else {
		se = &snapshotElement{}
	}
	se.seq, se.ref, se.prev = seq, 1, l.back
	if l.back != nil {
		l.back.next = se
	} else {
		l.front = se
	}
	l.back = se
	return se
}

func (l *snapshotList) remove(se *snapshotElement) {
	if se.prev != nil {
		se.prev.next = se.next
	} else {
		l.front = se.next
	}
	if se.next != nil {
		se.next.prev = se.prev
	} else {
		l.back = se.prev
	}
	se.prev, se.next = nil, nil
	l.free = se
}

// Acquires a snapshot, based on latest sequence.
func (db *DB) acquireSnapshot() *snapshotElement {
	db.snapsMu.Lock()
	defer db.snapsMu.Unlock()

	seq := db.getSeq()

	if se := db.snapsList.back; se != nil {
		if se.seq == seq {
			se.ref++
			return se
		} else if seq < se.seq {
			panic("leveldb: sequence number is not increasing")
		}
	}
	return db.snapsList.pushBack(seq)
}

// Releases given snapshot element. The element must not be used afterwards
// if this was its last reference, since it may be reused.
func (db *DB) releaseSnapshot(se *snapshotElement) {
	db.snapsMu.Lock()
	defer db.snapsMu.Unlock()

	se.ref--
	if se.ref == 0 {
		db.snapsList.remove(se)
	} else if se.ref < 0 {
		panic("leveldb: Snapshot: negative element reference")
	}
}

// Gets minimum sequence that not being snapshotted.
func (db *DB) minSeq() uint64 {
	db.snapsMu.Lock()
	defer db.snapsMu.Unlock()

	if se := db.snapsList.front; se != nil {
		return se.seq
	}

	return db.getSeq()
}

// Snapshot is a DB snapshot.
type Snapshot struct {
	db       *DB
	elem     *snapshotElement
	mu       sync.RWMutex
	released bool
}

// Creates new snapshot object.
func (db *DB) newSnapshot() *Snapshot {
	snap := &Snapshot{
		db:   db,
		elem: db.acquireSnapshot(),
	}
	atomic.AddInt32(&db.aliveSnaps, 1)
	runtime.SetFinalizer(snap, (*Snapshot).Release)
	return snap
}

func (snap *Snapshot) String() string {
	return fmt.Sprintf("leveldb.Snapshot{%d}", snap.elem.seq)
}

// Get gets the value for the given key. It returns ErrNotFound if
// the DB does not contains the key.
//
// The caller should not modify the contents of the returned slice, but
// it is safe to modify the contents of the argument after Get returns.
func (snap *Snapshot) Get(key []byte, ro *opt.ReadOptions) (value []byte, err error) {
	snap.mu.RLock()
	defer snap.mu.RUnlock()
	if snap.released {
		err = ErrSnapshotReleased
		return
	}
	err = snap.db.ok()
	if err != nil {
		return
	}
	return snap.db.get(nil, nil, key, snap.elem.seq, ro)
}

// Has returns true if the DB does contains the given key.
//
// It is safe to modify the contents of the argument after Get returns.
func (snap *Snapshot) Has(key []byte, ro *opt.ReadOptions) (ret bool, err error) {
	snap.mu.RLock()
	defer snap.mu.RUnlock()
	if snap.released {
		err = ErrSnapshotReleased
		return
	}
	err = snap.db.ok()
	if err != nil {
		return
	}
	return snap.db.has(nil, nil, key, snap.elem.seq, ro)
}

// NewIterator returns an iterator for the snapshot of the underlying DB.
// The returned iterator is not safe for concurrent use, but it is safe to use
// multiple iterators concurrently, with each in a dedicated goroutine.
// It is also safe to use an iterator concurrently with modifying its
// underlying DB. The resultant key/value pairs are guaranteed to be
// consistent.
//
// Slice allows slicing the iterator to only contains keys in the given
// range. A nil Range.Start is treated as a key before all keys in the
// DB. And a nil Range.Limit is treated as a key after all keys in
// the DB.
//
// WARNING: Any slice returned by interator (e.g. slice returned by calling
// Iterator.Key() or Iterator.Value() methods), its content should not be
// modified unless noted otherwise.
//
// The iterator must be released after use, by calling Release method.
// Releasing the snapshot doesn't mean releasing the iterator too, the
// iterator would be still valid until released.
//
// Also read Iterator documentation of the leveldb/iterator package.
func (snap *Snapshot) NewIterator(slice *util.Range, ro *opt.ReadOptions) iterator.Iterator {
	snap.mu.Lock()
	defer snap.mu.Unlock()
	if snap.released {
		return iterator.NewEmptyIterator(ErrSnapshotReleased)
	}
	if err := snap.db.ok(); err != nil {
		return iterator.NewEmptyIterator(err)
	}
	// Since iterator already hold version ref, it doesn't need to
	// hold snapshot ref.
	return snap.db.newIterator(nil, nil, snap.elem.seq, slice, ro)
}

// Release releases the snapshot. This will not release any returned
// iterators, the iterators would still be valid until released or the
// underlying DB is closed.
//
// Other methods should not be called after the snapshot has been released.
func (snap *Snapshot) Release() {
	snap.mu.Lock()
	defer snap.mu.Unlock()

	if !snap.released {
		// Clear the finalizer.
		runtime.SetFinalizer(snap, nil)

		snap.released = true
		snap.db.releaseSnapshot(snap.elem)
		atomic.AddInt32(&snap.db.aliveSnaps, -1)
		snap.db = nil
		snap.elem = nil
	}
}
