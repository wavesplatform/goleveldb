// Copyright (c) 2014, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package util

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// BufferPool is a 'buffer pool'.
type BufferPool struct {
	pool     [6]sync.Pool
	baseline [5]int
	// boxes holds empty *[]byte boxes taken out of pool by Get, for reuse
	// by Put.
	boxes sync.Pool

	get     uint32
	put     uint32
	less    uint32
	equal   uint32
	greater uint32
	miss    uint32
}

func (p *BufferPool) poolNum(n int) int {
	for i, x := range p.baseline {
		if n <= x {
			return i
		}
	}
	return len(p.baseline)
}

// Get returns buffer with length of n.
func (p *BufferPool) Get(n int) []byte {
	if p == nil {
		return make([]byte, n)
	}
	atomic.AddUint32(&p.get, 1)

	poolNum := p.poolNum(n)

	box := p.pool[poolNum].Get().(*[]byte)
	b := *box
	// Recycle the box, so that Put doesn't allocate a new one.
	*box = nil
	p.boxes.Put(box)

	if cap(b) == 0 {
		// If we grabbed nothing, increment the miss stats.
		atomic.AddUint32(&p.miss, 1)

		if poolNum == len(p.baseline) {
			return make([]byte, n)
		}
		return make([]byte, p.baseline[poolNum])[:n]
	} else {
		// If there is enough capacity in the bytes grabbed, resize the length
		// to n and return.
		if n < cap(b) {
			atomic.AddUint32(&p.less, 1)
			return b[:n]
		} else if n == cap(b) {
			atomic.AddUint32(&p.equal, 1)
			return b[:n]
		} else if n > cap(b) {
			atomic.AddUint32(&p.greater, 1)
		}
	}

	if poolNum == len(p.baseline) {
		return make([]byte, n)
	}
	return make([]byte, p.baseline[poolNum])[:n]
}

// Put adds given buffer to the pool.
func (p *BufferPool) Put(b []byte) {
	if p == nil {
		return
	}

	poolNum := p.poolNum(cap(b))

	atomic.AddUint32(&p.put, 1)
	box, _ := p.boxes.Get().(*[]byte)
	if box == nil {
		box = new([]byte)
	}
	*box = b
	p.pool[poolNum].Put(box)
}

func (p *BufferPool) String() string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("BufferPool{B·%d G·%d P·%d <·%d =·%d >·%d M·%d}",
		p.baseline, p.get, p.put, p.less, p.equal, p.greater, p.miss)
}

// NewBufferPool creates a new initialized 'buffer pool'.
func NewBufferPool(baseline int) *BufferPool {
	if baseline <= 0 {
		panic("baseline can't be <= 0")
	}
	bufPool := &BufferPool{
		baseline: [...]int{baseline / 4, baseline / 2, baseline, baseline * 2, baseline * 4},
		pool: [6]sync.Pool{
			{
				New: func() any { return new([]byte) },
			},
			{
				New: func() any { return new([]byte) },
			},
			{
				New: func() any { return new([]byte) },
			},
			{
				New: func() any { return new([]byte) },
			},
			{
				New: func() any { return new([]byte) },
			},
			{
				New: func() any { return new([]byte) },
			},
		},
	}

	return bufPool
}
