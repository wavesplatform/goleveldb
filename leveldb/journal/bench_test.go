package journal

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

// discardFlusher is an in-memory io.Writer that also implements Flush, like
// the journal file writer used by the DB.
type discardFlusher struct {
	n, flushes int64
}

func (d *discardFlusher) Write(p []byte) (int, error) {
	d.n += int64(len(p))
	return len(p), nil
}

func (d *discardFlusher) Flush() error {
	d.flushes++
	return nil
}

var benchRecordSizes = []int{100, 1024, 4096, 64 * 1024}

func benchRecord(size int) []byte {
	return []byte(big("journal benchmark record ", size))
}

func BenchmarkWriter(b *testing.B) {
	for _, size := range benchRecordSizes {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			rec := benchRecord(size)
			w := NewWriter(&discardFlusher{})
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				jw, err := w.Next()
				if err != nil {
					b.Fatal("next: ", err)
				}
				if _, err := jw.Write(rec); err != nil {
					b.Fatal("write: ", err)
				}
				if err := w.Flush(); err != nil {
					b.Fatal("flush: ", err)
				}
			}
		})
	}
}

// BenchmarkWriterNoFlush appends records without flushing after each one, so
// the writer only writes out whole blocks.
func BenchmarkWriterNoFlush(b *testing.B) {
	for _, size := range benchRecordSizes {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			rec := benchRecord(size)
			w := NewWriter(&discardFlusher{})
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				jw, err := w.Next()
				if err != nil {
					b.Fatal("next: ", err)
				}
				if _, err := jw.Write(rec); err != nil {
					b.Fatal("write: ", err)
				}
			}
			if err := w.Close(); err != nil {
				b.Fatal("close: ", err)
			}
		})
	}
}

func BenchmarkReader(b *testing.B) {
	const records = 1000
	for _, size := range benchRecordSizes {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			rec := benchRecord(size)
			buf := new(bytes.Buffer)
			w := NewWriter(buf)
			for range records {
				jw, err := w.Next()
				if err != nil {
					b.Fatal("next: ", err)
				}
				if _, err := jw.Write(rec); err != nil {
					b.Fatal("write: ", err)
				}
			}
			if err := w.Close(); err != nil {
				b.Fatal("close: ", err)
			}
			data := buf.Bytes()

			src := bytes.NewReader(data)
			r := NewReader(src, nil, true, true)
			dst := make([]byte, size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				jr, err := r.Next()
				if err == io.EOF {
					src.Reset(data)
					// Reset returns the last error, which is io.EOF here.
					if err := r.Reset(src, nil, true, true); err != io.EOF {
						b.Fatal("reset: ", err)
					}
					jr, err = r.Next()
				}
				if err != nil {
					b.Fatal("next: ", err)
				}
				if _, err := io.ReadFull(jr, dst); err != nil {
					b.Fatal("read: ", err)
				}
			}
		})
	}
}
