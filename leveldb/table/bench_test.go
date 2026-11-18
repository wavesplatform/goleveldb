package table

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	"github.com/wavesplatform/goleveldb/leveldb/cache"
	"github.com/wavesplatform/goleveldb/leveldb/filter"
	"github.com/wavesplatform/goleveldb/leveldb/opt"
	"github.com/wavesplatform/goleveldb/leveldb/storage"
	"github.com/wavesplatform/goleveldb/leveldb/util"
)

const (
	benchTableEntries = 10000
	benchValueLen     = 100
)

// benchKV returns sorted keys and half-compressible values for a table.
func benchKV() (keys, values [][]byte) {
	r := rand.New(rand.NewSource(301))
	keys = make([][]byte, benchTableEntries)
	values = make([][]byte, benchTableEntries)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "%016d", i*2)
		v := make([]byte, benchValueLen)
		for j := range benchValueLen / 2 {
			v[j] = ' ' + byte(r.Intn(95))
		}
		copy(v[benchValueLen/2:], v[:benchValueLen/2])
		values[i] = v
	}
	return keys, values
}

func benchTableOptions(compression opt.Compression, bloom bool) *opt.Options {
	o := &opt.Options{Compression: compression}
	if bloom {
		o.Filter = filter.NewBloomFilter(10)
	}
	return o
}

func buildBenchTable(tb testing.TB, buf *bytes.Buffer, o *opt.Options, bpool *util.BufferPool, keys, values [][]byte) {
	tw := NewWriter(buf, o, bpool, 0)
	for i := range keys {
		if err := tw.Append(keys[i], values[i]); err != nil {
			tb.Fatal("append: ", err)
		}
	}
	if err := tw.Close(); err != nil {
		tb.Fatal("close: ", err)
	}
}

var benchCompressions = []struct {
	name string
	c    opt.Compression
}{
	{"none", opt.NoCompression},
	{"snappy", opt.SnappyCompression},
	{"minlz", opt.MinLZCompression},
}

func BenchmarkTableWriter(b *testing.B) {
	keys, values := benchKV()
	for _, c := range benchCompressions {
		for _, bloom := range []bool{false, true} {
			name := c.name
			if bloom {
				name += "-bloom"
			}
			b.Run(name, func(b *testing.B) {
				o := benchTableOptions(c.c, bloom)
				bpool := util.NewBufferPool(o.GetBlockSize())
				buf := new(bytes.Buffer)
				b.SetBytes(int64(benchTableEntries * (len(keys[0]) + benchValueLen)))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					buf.Reset()
					buildBenchTable(b, buf, o, bpool, keys, values)
				}
			})
		}
	}
}

type benchTable struct {
	r            *Reader
	keys, values [][]byte
	missing      [][]byte
	order        []int
}

// openBenchTable builds an in-memory table and opens a Reader on it. With
// cached set the Reader gets a block cache and a buffer pool, as in the DB.
func openBenchTable(tb testing.TB, compression opt.Compression, bloom, cached bool) *benchTable {
	keys, values := benchKV()
	o := benchTableOptions(compression, bloom)
	buf := new(bytes.Buffer)
	buildBenchTable(tb, buf, o, nil, keys, values)

	var (
		ns    *cache.NamespaceGetter
		bpool *util.BufferPool
	)
	if cached {
		ns = &cache.NamespaceGetter{Cache: cache.NewCache(cache.NewLRU(8 * opt.MiB))}
		bpool = util.NewBufferPool(o.GetBlockSize() + 5)
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), storage.FileDesc{}, ns, bpool, o)
	if err != nil {
		tb.Fatal("new reader: ", err)
	}

	t := &benchTable{r: r, keys: keys, values: values}
	t.missing = make([][]byte, len(keys))
	for i := range keys {
		// Keys are even numbers, so odd numbers are absent from the table.
		t.missing[i] = fmt.Appendf(nil, "%016d", i*2+1)
	}
	t.order = rand.New(rand.NewSource(0xdeadbeef)).Perm(len(keys))
	tb.Cleanup(r.Release)
	return t
}

func (t *benchTable) key(i int) []byte {
	return t.keys[t.order[i%len(t.order)]]
}

func benchTableReader(b *testing.B, fn func(b *testing.B, t *benchTable)) {
	for _, cached := range []bool{true, false} {
		name := "cached"
		if !cached {
			name = "uncached"
		}
		b.Run(name, func(b *testing.B) {
			t := openBenchTable(b, opt.SnappyCompression, true, cached)
			b.ReportAllocs()
			b.ResetTimer()
			fn(b, t)
		})
	}
}

func BenchmarkTableReaderGet(b *testing.B) {
	benchTableReader(b, func(b *testing.B, t *benchTable) {
		for i := range b.N {
			if _, err := t.r.Get(t.key(i), nil); err != nil {
				b.Fatal("get: ", err)
			}
		}
	})
}

func BenchmarkTableReaderGetMissing(b *testing.B) {
	benchTableReader(b, func(b *testing.B, t *benchTable) {
		for i := range b.N {
			key := t.missing[t.order[i%len(t.order)]]
			if _, err := t.r.Get(key, nil); err == nil {
				b.Fatal("get: unexpected key found")
			}
		}
	})
}

func BenchmarkTableReaderFind(b *testing.B) {
	benchTableReader(b, func(b *testing.B, t *benchTable) {
		for i := range b.N {
			if _, _, err := t.r.Find(t.key(i), false, nil); err != nil {
				b.Fatal("find: ", err)
			}
		}
	})
}

func BenchmarkTableReaderIterSeek(b *testing.B) {
	benchTableReader(b, func(b *testing.B, t *benchTable) {
		iter := t.r.NewIterator(nil, nil)
		defer iter.Release()
		for i := range b.N {
			if !iter.Seek(t.key(i)) {
				b.Fatal("seek: key not found")
			}
		}
	})
}

func BenchmarkTableReaderIterNext(b *testing.B) {
	benchTableReader(b, func(b *testing.B, t *benchTable) {
		iter := t.r.NewIterator(nil, nil)
		defer iter.Release()
		for range b.N {
			if !iter.Next() {
				if err := iter.Error(); err != nil {
					b.Fatal("next: ", err)
				}
				iter.First()
			}
		}
	})
}
