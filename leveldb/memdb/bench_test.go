// Copyright (c) 2012, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package memdb

import (
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/wavesplatform/goleveldb/leveldb/comparer"
)

func BenchmarkPut(b *testing.B) {
	buf := make([][4]byte, b.N)
	for i := range buf {
		binary.LittleEndian.PutUint32(buf[i][:], uint32(i))
	}

	b.ResetTimer()
	p := New(comparer.DefaultComparer, 0)
	for i := range buf {
		if err := p.Put(buf[i][:], nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPutRandom(b *testing.B) {
	buf := make([][4]byte, b.N)
	for i := range buf {
		binary.LittleEndian.PutUint32(buf[i][:], uint32(rand.Int()))
	}

	b.ResetTimer()
	p := New(comparer.DefaultComparer, 0)
	for i := range buf {
		if err := p.Put(buf[i][:], nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGet(b *testing.B) {
	buf := make([][4]byte, b.N)
	for i := range buf {
		binary.LittleEndian.PutUint32(buf[i][:], uint32(i))
	}

	p := New(comparer.DefaultComparer, 0)
	for i := range buf {
		if err := p.Put(buf[i][:], nil); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := range buf {
		if _, err := p.Get(buf[i][:]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetRandom(b *testing.B) {
	buf := make([][4]byte, b.N)
	for i := range buf {
		binary.LittleEndian.PutUint32(buf[i][:], uint32(i))
	}

	p := New(comparer.DefaultComparer, 0)
	for i := range buf {
		if err := p.Put(buf[i][:], nil); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for range b.N {
		if _, err := p.Get(buf[rand.Int()%len(buf)][:]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPutCapacity fills a memdb created with a fixed capacity, like the
// DB write buffer, starting over with a new memdb when it is full.
func BenchmarkPutCapacity(b *testing.B) {
	const capacity = 4 << 20
	key := make([]byte, 16)
	value := make([]byte, 100)
	b.ReportAllocs()
	p := New(comparer.DefaultComparer, capacity)
	i := 0
	for b.Loop() {
		if p.Free() < len(key)+len(value) {
			p = New(comparer.DefaultComparer, capacity)
		}
		binary.BigEndian.PutUint64(key, uint64(i))
		i++
		if err := p.Put(key, value); err != nil {
			b.Fatal(err)
		}
	}
}
