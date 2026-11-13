// Copyright (c) 2012, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package util

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"
)

var hashTests = []struct {
	data []byte
	seed uint32
	hash uint32
}{
	{nil, 0xbc9f1d34, 0xbc9f1d34},
	{[]byte{0x62}, 0xbc9f1d34, 0xef1345c4},
	{[]byte{0xc3, 0x97}, 0xbc9f1d34, 0x5b663814},
	{[]byte{0xe2, 0x99, 0xa5}, 0xbc9f1d34, 0x323c078f},
	{[]byte{0xe1, 0x80, 0xb9, 0x32}, 0xbc9f1d34, 0xed21633a},
	{[]byte{
		0x01, 0xc0, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x14, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x04, 0x00,
		0x00, 0x00, 0x00, 0x14,
		0x00, 0x00, 0x00, 0x18,
		0x28, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}, 0x12345678, 0xf333dabb},
}

func TestHash(t *testing.T) {
	for i, x := range hashTests {
		h := Hash(x.data, x.seed)
		if h != x.hash {
			t.Fatalf("test-%d: invalid hash, %#x vs %#x", i, h, x.hash)
		}
	}
}

// refHash is the original index-based implementation of Hash.
func refHash(data []byte, seed uint32) uint32 {
	const (
		m = uint32(0xc6a4a793)
		r = uint32(24)
	)
	var (
		h = seed ^ (uint32(len(data)) * m)
		i int
	)
	for n := len(data) - len(data)%4; i < n; i += 4 {
		h += binary.LittleEndian.Uint32(data[i:])
		h *= m
		h ^= (h >> 16)
	}
	switch len(data) - i {
	case 3:
		h += uint32(data[i+2]) << 16
		fallthrough
	case 2:
		h += uint32(data[i+1]) << 8
		fallthrough
	case 1:
		h += uint32(data[i])
		h *= m
		h ^= (h >> r)
	}
	return h
}

func TestHashMatchesReference(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	data := make([]byte, 100)
	for i := range 10000 {
		b := data[:i%len(data)]
		rnd.Read(b)
		seed := rnd.Uint32()
		if got, want := Hash(b, seed), refHash(b, seed); got != want {
			t.Fatalf("len=%d seed=%#x: hash %#x, want %#x", len(b), seed, got, want)
		}
	}
}

var hashSink uint32

func BenchmarkHash(b *testing.B) {
	for _, n := range []int{7, 16, 25, 100} {
		data := make([]byte, n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for range b.N {
				hashSink += Hash(data, 0xbc9f1d34)
			}
		})
	}
}
