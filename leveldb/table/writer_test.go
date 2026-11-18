package table

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/wavesplatform/goleveldb/leveldb/filter"
	"github.com/wavesplatform/goleveldb/leveldb/opt"
	"github.com/wavesplatform/goleveldb/leveldb/util"
)

// goldenTableKV returns a fixed input with shared key prefixes of varying
// length, empty and long values.
func goldenTableKV() (keys, values [][]byte) {
	r := rand.New(rand.NewSource(7))
	for i := range 3000 {
		key := fmt.Appendf(nil, "%08d", i*3)
		key = append(key, bytes.Repeat([]byte{'k'}, r.Intn(20))...)
		var v []byte
		switch i % 7 {
		case 0:
		case 1:
			v = bytes.Repeat([]byte{byte(i)}, 3000)
		default:
			v = make([]byte, r.Intn(200))
			for j := range v {
				v[j] = 'a' + byte(r.Intn(4))
			}
		}
		keys = append(keys, key)
		values = append(values, v)
	}
	return keys, values
}

func goldenTableCases() map[string]*opt.Options {
	cases := make(map[string]*opt.Options)
	for _, c := range []struct {
		name string
		c    opt.Compression
	}{
		{"none", opt.NoCompression},
		{"snappy", opt.SnappyCompression},
		{"zstd", opt.ZSTDCompression},
		{"minlz", opt.MinLZCompression},
	} {
		cases[c.name] = &opt.Options{Compression: c.c}
		cases[c.name+"-bloom"] = &opt.Options{Compression: c.c, Filter: filter.NewBloomFilter(10)}
		cases[c.name+"-small"] = &opt.Options{
			Compression:          c.c,
			Filter:               filter.NewBloomFilter(7),
			BlockSize:            1024,
			BlockRestartInterval: 3,
			FilterBaseLg:         10,
		}
	}
	return cases
}

// goldenTableSums are the SHA-256 sums of the tables built from
// goldenTableKV, recorded before the writer optimizations. The table format
// must not change, so any difference is a bug.
var goldenTableSums = map[string]string{
	"none":         "f29f735be000e3ced851d9513cc9d43b57011bb3280e71250554cb0d0e021c8b",
	"none-bloom":   "8a1d1fa030dd2197caa3f4fb16c845c0ddbcbfc0c2ae7ef039cc3c9fbce8f5a5",
	"none-small":   "8e4211ba152581d7f3268dbd27fdf8916ba9deb4d2d467c9ee312c7dff7805dd",
	"snappy":       "e2892909faadc1d4dc19f8f2902d40994441c23197c5ce1fdedfe56ac6eedd32",
	"snappy-bloom": "1da86d823c7ab48a196ea463a59928797b92787150be04b9620b1171318a98ef",
	"snappy-small": "7f8b993744c8689fd919266533b7c978c65af1802121374330f6e28edf90e18d",
	"zstd":         "bf71a3f890a4e20f56cbc482e908ca982a08e697af29c9c8a3b9178c13ef5417",
	"zstd-bloom":   "f1791efa4c1bafd79e1f3a969a11ce6cdddc70b62366c599ac7403f1f91ce1dc",
	"zstd-small":   "24ba2b03d8e95ae5fc2ba6dedd3bd303cfeafc757d121a1ffa08177daab45739",
	"minlz":        "76f801c293d8f9828ade13e2e821f79c3c4e609044f7190bb5a3e56aeb541ab2",
	"minlz-bloom":  "92fa7d37119306c27232da9b24ad5b3f23b3e34fde26c6397b651dbeee8d8387",
	"minlz-small":  "7dcde3ea75e5077da547cb92de7ccfc43b5b9db27c32d1f806a05d4c0139bf7d",
}

func TestWriterOutputGolden(t *testing.T) {
	keys, values := goldenTableKV()
	cases := goldenTableCases()
	if len(cases) != len(goldenTableSums) {
		t.Fatalf("got %d cases, want %d", len(cases), len(goldenTableSums))
	}
	bpool := util.NewBufferPool(opt.DefaultBlockSize)
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			// Build twice with a shared buffer pool, so that reused buffers
			// are covered too.
			for range 2 {
				buf := new(bytes.Buffer)
				buildBenchTable(t, buf, o, bpool, keys, values)
				sum := sha256.Sum256(buf.Bytes())
				if got := hex.EncodeToString(sum[:]); got != goldenTableSums[name] {
					t.Fatalf("table sha256 %s, want %s", got, goldenTableSums[name])
				}
			}
		})
	}
}

// Once the block buffers have grown, appending to a block does not allocate.
func TestBlockWriterAppendAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not stable under the race detector")
	}
	keys, values := benchKV()
	w := &blockWriter{restartInterval: opt.DefaultBlockRestartInterval, scratch: make([]byte, 30)}
	fill := func() {
		w.reset()
		for i := range 200 {
			if err := w.append(keys[i], values[i]); err != nil {
				t.Fatal("append: ", err)
			}
		}
		if err := w.finish(); err != nil {
			t.Fatal("finish: ", err)
		}
	}
	fill()
	if n := testing.AllocsPerRun(100, fill); n != 0 {
		t.Errorf("got %v allocs per block, want 0", n)
	}
}

// Building a table allocates a fixed number of buffers, independent of the
// number of entries: the writer itself, the index and filter blocks growing,
// and compression scratch.
func TestWriterAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not stable under the race detector")
	}
	keys, values := benchKV()
	for _, c := range benchCompressions {
		for _, bloom := range []bool{false, true} {
			name := c.name
			if bloom {
				name += "-bloom"
			}
			t.Run(name, func(t *testing.T) {
				o := benchTableOptions(c.c, bloom)
				bpool := util.NewBufferPool(o.GetBlockSize())
				buf := new(bytes.Buffer)
				build := func() {
					buf.Reset()
					buildBenchTable(t, buf, o, bpool, keys, values)
				}
				build()
				// About 10 today, one allocation per 100 entries would be 100.
				if n := testing.AllocsPerRun(20, build); n > 16 {
					t.Errorf("got %v allocs per table of %d entries, want <= 16", n, len(keys))
				}
			})
		}
	}
}

var errFailingWriter = errors.New("failing writer")

// failingWriter fails once n bytes have been written.
type failingWriter struct {
	n int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		w.n = 0
		return 0, errFailingWriter
	}
	w.n -= len(p)
	return len(p), nil
}

// A write error is returned by Close, also when Close is called again, and
// the buffers released by the failed writer don't affect the next table.
func TestWriterCloseAfterWriteError(t *testing.T) {
	keys, values := goldenTableKV()
	const name = "snappy-small"
	o := goldenTableCases()[name]
	bpool := util.NewBufferPool(opt.DefaultBlockSize)

	full := new(bytes.Buffer)
	buildBenchTable(t, full, o, bpool, keys, values)
	size := full.Len()
	// Fail in the first data block, mid-table, in the filter, index and
	// metaindex blocks and in the footer.
	for _, n := range []int{0, size / 2, size - 4096, size - 200, size - footerLen - 1, size - 1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			tw := NewWriter(&failingWriter{n: n}, o, bpool, 0)
			var err error
			for i := range keys {
				if err = tw.Append(keys[i], values[i]); err != nil {
					break
				}
			}
			if cerr := tw.Close(); !errors.Is(cerr, errFailingWriter) {
				t.Fatalf("close: got %v, want %v (append error %v)", cerr, errFailingWriter, err)
			}
			if cerr := tw.Close(); !errors.Is(cerr, errFailingWriter) {
				t.Fatalf("second close: got %v, want %v", cerr, errFailingWriter)
			}

			buf := new(bytes.Buffer)
			buildBenchTable(t, buf, o, bpool, keys, values)
			if !bytes.Equal(buf.Bytes(), full.Bytes()) {
				t.Fatal("table built after a failed writer differs")
			}
		})
	}
}

// A filter buffer recycled from a larger table with other keys must not
// leak set bits into the next table's filter.
func TestWriterFilterBufferReuse(t *testing.T) {
	keys, values := goldenTableKV()
	const name = "none-bloom"
	o := goldenTableCases()[name]

	var otherKeys, otherValues [][]byte
	for i := range 3 * len(keys) {
		otherKeys = append(otherKeys, fmt.Appendf(nil, "%08d-other", i))
		otherValues = append(otherValues, nil)
	}
	for range 3 {
		buildBenchTable(t, new(bytes.Buffer), o, nil, otherKeys, otherValues)
		buf := new(bytes.Buffer)
		buildBenchTable(t, buf, o, nil, keys, values)
		sum := sha256.Sum256(buf.Bytes())
		if got := hex.EncodeToString(sum[:]); got != goldenTableSums[name] {
			t.Fatalf("table sha256 %s, want %s", got, goldenTableSums[name])
		}
	}
}
