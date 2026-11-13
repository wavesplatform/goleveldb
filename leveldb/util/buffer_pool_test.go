// Copyright (c) 2014, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package util

import (
	"sync"
	"testing"
)

func TestBufferPoolGetPut(t *testing.T) {
	p := NewBufferPool(1024)
	for _, n := range []int{0, 1, 255, 256, 257, 1024, 4096, 4097, 10000} {
		b := p.Get(n)
		if len(b) != n {
			t.Fatalf("Get(%d): got len %d", n, len(b))
		}
		for i := range b {
			b[i] = byte(i)
		}
		p.Put(b)
		if b2 := p.Get(n); len(b2) != n {
			t.Fatalf("Get(%d) after Put: got len %d", n, len(b2))
		}
	}

	var nilPool *BufferPool
	if b := nilPool.Get(10); len(b) != 10 {
		t.Fatalf("nil pool Get: got len %d", len(b))
	}
	nilPool.Put(make([]byte, 10))
}

// TestBufferPoolConcurrent checks that buffers handed out concurrently are
// not shared, now that Get and Put recycle the boxes they are pooled in.
func TestBufferPoolConcurrent(t *testing.T) {
	p := NewBufferPool(1024)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 2000 {
				n := 1 + (g*131+i*17)%3000
				b := p.Get(n)
				for j := range b {
					b[j] = byte(g)
				}
				for j := range b {
					if b[j] != byte(g) {
						t.Errorf("buffer shared between goroutines")
						return
					}
				}
				p.Put(b)
			}
		})
	}
	wg.Wait()
}

// TestBufferPoolAllocs locks in that a Get/Put round trip of a pooled
// buffer doesn't allocate.
func TestBufferPoolAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not stable under the race detector")
	}
	p := NewBufferPool(1024)
	p.Put(p.Get(1000))
	if n := testing.AllocsPerRun(1000, func() {
		p.Put(p.Get(1000))
	}); n > 0 {
		t.Errorf("Get/Put: got %v allocs/op, want 0", n)
	}
}

func BenchmarkBufferPoolGetPut(b *testing.B) {
	p := NewBufferPool(4096)
	b.ReportAllocs()
	for b.Loop() {
		p.Put(p.Get(4000))
	}
}
