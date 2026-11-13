// Copyright (c) 2012, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package table

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sync"

	"github.com/golang/snappy"
	"github.com/klauspost/compress/zstd"
	"github.com/minio/minlz"

	"github.com/wavesplatform/goleveldb/leveldb/comparer"
	"github.com/wavesplatform/goleveldb/leveldb/filter"
	"github.com/wavesplatform/goleveldb/leveldb/opt"
	"github.com/wavesplatform/goleveldb/leveldb/util"
)

func sharedPrefixLen(a, b []byte) int {
	i, n := 0, len(a)
	if n > len(b) {
		n = len(b)
	}
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

type blockWriter struct {
	restartInterval int
	buf             util.Buffer
	nEntries        int
	prevKey         []byte
	restarts        []uint32
	scratch         []byte
}

func (w *blockWriter) append(key, value []byte) (err error) {
	nShared := 0
	if w.nEntries%w.restartInterval == 0 {
		w.restarts = append(w.restarts, uint32(w.buf.Len()))
	} else {
		nShared = sharedPrefixLen(w.prevKey, key)
	}
	unshared := key[nShared:]
	n := binary.PutUvarint(w.scratch[0:], uint64(nShared))
	n += binary.PutUvarint(w.scratch[n:], uint64(len(unshared)))
	n += binary.PutUvarint(w.scratch[n:], uint64(len(value)))
	// Grow the buffer once for the whole entry.
	b := w.buf.Alloc(n + len(unshared) + len(value))
	copy(b[copy(b, w.scratch[:n]):], unshared)
	copy(b[n+len(unshared):], value)
	// The shared prefix is already in prevKey.
	w.prevKey = append(w.prevKey[:nShared], unshared...)
	w.nEntries++
	return nil
}

func (w *blockWriter) finish() error {
	// Write restarts entry.
	if w.nEntries == 0 {
		// Must have at least one restart entry.
		w.restarts = append(w.restarts, 0)
	}
	w.restarts = append(w.restarts, uint32(len(w.restarts)))
	for _, x := range w.restarts {
		buf4 := w.buf.Alloc(4)
		binary.LittleEndian.PutUint32(buf4, x)
	}
	return nil
}

func (w *blockWriter) reset() {
	w.buf.Reset()
	w.nEntries = 0
	w.restarts = w.restarts[:0]
}

func (w *blockWriter) bytesLen() int {
	restartsLen := len(w.restarts)
	if restartsLen == 0 {
		restartsLen = 1
	}
	return w.buf.Len() + 4*restartsLen + 4
}

type filterWriter struct {
	generator filter.FilterGenerator
	buf       util.Buffer
	nKeys     int
	offsets   []uint32
	baseLg    uint
}

func (w *filterWriter) add(key []byte) {
	if w.generator == nil {
		return
	}
	w.generator.Add(key)
	w.nKeys++
}

func (w *filterWriter) flush(offset uint64) {
	if w.generator == nil {
		return
	}
	for x := int(offset / uint64(1<<w.baseLg)); x > len(w.offsets); {
		w.generate()
	}
}

func (w *filterWriter) finish() error {
	if w.generator == nil {
		return nil
	}
	// Generate last keys.

	if w.nKeys > 0 {
		w.generate()
	}
	w.offsets = append(w.offsets, uint32(w.buf.Len()))
	for _, x := range w.offsets {
		buf4 := w.buf.Alloc(4)
		binary.LittleEndian.PutUint32(buf4, x)
	}
	return w.buf.WriteByte(byte(w.baseLg))
}

func (w *filterWriter) generate() {
	// Record offset.
	w.offsets = append(w.offsets, uint32(w.buf.Len()))
	// Generate filters.
	if w.nKeys > 0 {
		w.generator.Generate(&w.buf)
		w.nKeys = 0
	}
}

// writerBufs holds the buffers of a Writer that grow with the table. They are
// recycled through writerBufsPool when the Writer is closed, so that building
// many tables, as compaction does, does not grow them again for each table.
type writerBufs struct {
	index, filter         []byte
	dataPrevKey, indexKey []byte
	compression           []byte
	dataRestarts          []uint32
	indexRestarts         []uint32
	filterOffsets         []uint32
}

// maxPooledWriterBuf is the largest buffer kept in writerBufsPool, larger
// ones are left to the GC.
const maxPooledWriterBuf = 1 << 20

var writerBufsPool = sync.Pool{
	New: func() any { return new(writerBufs) },
}

// reusable returns s emptied for reuse, or nil if its capacity exceeds
// maxCap elements.
func reusable[T any](s []T, maxCap int) []T {
	if cap(s) > maxCap {
		return nil
	}
	return s[:0]
}

func pooledBytes(b []byte) []byte { return reusable(b, maxPooledWriterBuf) }

func pooledUint32s(b []uint32) []uint32 { return reusable(b, maxPooledWriterBuf/4) }

var errWriterClosed = errors.New("leveldb/table: writer is closed")

// Writer is a table writer.
type Writer struct {
	writer io.Writer
	err    error
	// Options
	cmp         comparer.Comparer
	filter      filter.Filter
	compression opt.Compression
	blockSize   int

	bpool       *util.BufferPool
	dataBlock   blockWriter
	indexBlock  blockWriter
	filterBlock filterWriter
	pendingBH   blockHandle
	offset      uint64
	nEntries    int
	// Scratch allocated enough for 5 uvarint. Block writer should not use
	// first 20-bytes since it will be used to encode block handle, which
	// then passed to the block writer itself.
	scratch            [50]byte
	comparerScratch    []byte
	compressionScratch []byte
	// bufs is returned to writerBufsPool by release, nil afterwards.
	bufs *writerBufs
}

// release returns the data block buffer to the buffer pool and the other
// growing buffers to writerBufsPool. It is called once, by Close.
func (w *Writer) release() {
	if w.bpool != nil {
		// Buffer.Bytes() returns [offset:] of the buffer.
		// We need to Reset() so that the offset = 0, resulting
		// in buf.Bytes() returning the whole allocated bytes.
		w.dataBlock.buf.Reset()
		w.bpool.Put(w.dataBlock.buf.Bytes())
	}
	w.dataBlock.buf = util.Buffer{}

	b := w.bufs
	w.bufs = nil
	w.indexBlock.buf.Reset()
	b.index = pooledBytes(w.indexBlock.buf.Bytes())
	w.indexBlock.buf = util.Buffer{}
	w.filterBlock.buf.Reset()
	// Filter generators may expect memory from Alloc to be zeroed, as it is
	// in a new buffer, the bloom filter sets bits in it.
	b.filter = pooledBytes(w.filterBlock.buf.Bytes())
	clear(b.filter[:cap(b.filter)])
	w.filterBlock.buf = util.Buffer{}
	b.dataPrevKey, w.dataBlock.prevKey = pooledBytes(w.dataBlock.prevKey), nil
	b.indexKey, w.indexBlock.prevKey = pooledBytes(w.indexBlock.prevKey), nil
	b.compression, w.compressionScratch = pooledBytes(w.compressionScratch), nil
	b.dataRestarts, w.dataBlock.restarts = pooledUint32s(w.dataBlock.restarts), nil
	b.indexRestarts, w.indexBlock.restarts = pooledUint32s(w.indexBlock.restarts), nil
	b.filterOffsets, w.filterBlock.offsets = pooledUint32s(w.filterBlock.offsets), nil
	writerBufsPool.Put(b)
}

func (w *Writer) writeBlock(buf *util.Buffer, compression opt.Compression) (bh blockHandle, err error) {
	// Compress the buffer if necessary.
	var b []byte
	switch compression {
	case opt.SnappyCompression:
		// Allocate scratch enough for compression and block trailer.
		// snappy.Encode uses len(dst), the scratch may come from the pool
		// with zero length.
		if n := snappy.MaxEncodedLen(buf.Len()) + blockTrailerLen; cap(w.compressionScratch) < n {
			w.compressionScratch = make([]byte, n)
		}
		w.compressionScratch = w.compressionScratch[:cap(w.compressionScratch)]
		compressed := snappy.Encode(w.compressionScratch, buf.Bytes())
		n := len(compressed)
		b = compressed[:n+blockTrailerLen]
		b[n] = blockTypeSnappyCompression
	case opt.ZSTDCompression:
		w.compressionScratch = zstd.EncodeTo(w.compressionScratch[:0], buf.Bytes())
		n := len(w.compressionScratch)        // compressed size
		size := n + blockTrailerLen           // total size with trailer
		if cap(w.compressionScratch) < size { // slow path: can't grow by reslice, need to allocate new
			old := w.compressionScratch
			w.compressionScratch = make([]byte, size) // allocate the needed size
			copy(w.compressionScratch, old)           // copy compressed data
		}
		b = w.compressionScratch[:size]
		b[n] = blockTypeZSTDCompression // set block type before checksum
	case opt.MinLZCompression:
		w.compressionScratch, err = minLZEncodeTo(w.compressionScratch[:0], buf.Bytes())
		if err != nil {
			return bh, fmt.Errorf("leveldb/table: Writer: minlz compression failed: %w", err)
		}
		n := len(w.compressionScratch)        // compressed size
		size := n + blockTrailerLen           // total size with trailer
		if cap(w.compressionScratch) < size { // slow path: can't grow by reslice, need to allocate new
			old := w.compressionScratch
			w.compressionScratch = make([]byte, size) // allocate the needed size
			copy(w.compressionScratch, old)           // copy compressed data
		}
		b = w.compressionScratch[:size]
		b[n] = blockTypeMinLZCompression // set block type before checksum
	case opt.NoCompression:
		tmp := buf.Alloc(blockTrailerLen)
		tmp[0] = blockTypeNoCompression
		b = buf.Bytes()
	default:
		return bh, fmt.Errorf("leveldb/table: Writer: unknown compression type: %v", compression)
	}

	// Calculate the checksum.
	n := len(b) - 4
	checksum := util.NewCRC(b[:n]).Value()
	binary.LittleEndian.PutUint32(b[n:], checksum)

	// Write the buffer to the file.
	_, err = w.writer.Write(b)
	if err != nil {
		return
	}
	bh = blockHandle{w.offset, uint64(len(b) - blockTrailerLen)}
	w.offset += uint64(len(b))
	return
}

var _ = map[bool]struct{}{false: {}, math.MaxUint32 > minlz.MaxBlockSize: {}} // compile-time assert

// minLZEncodeTo appends the encoded data from src to dst.
// It returns the compressed data slice (which may share the same underlying array as dst) or an error.
// If dst is not large enough, a new slice will be allocated.
func minLZEncodeTo(dst, src []byte) ([]byte, error) {
	dst = dst[:0]
	var (
		err error
		pos int
	)
	// split src into chunks of max minlz.MaxBlockSize size because minlz can only handle that much data at once
	for srcPart := range slices.Chunk(src, minlz.MaxBlockSize) {
		dst = slices.Grow(dst, uint32Size)[:len(dst)+uint32Size] // Reserve space for length prefix
		// Fastest compression level has been chosen because it gives decent compression ratio
		// with very fast compression speed. (better than snappy in both aspects)
		dst, err = minlz.AppendEncoded(dst, srcPart, minlz.LevelFastest)
		if err != nil {
			return nil, err
		}
		// Write length prefix
		compressedSize := uint32(len(dst[pos:]) - uint32Size)
		binary.LittleEndian.PutUint32(dst[pos:], compressedSize)
		pos = len(dst) // Move position to the end of the compressed data
	}
	return dst, nil
}

func (w *Writer) flushPendingBH(key []byte) error {
	if w.pendingBH.length == 0 {
		return nil
	}
	var separator []byte
	if len(key) == 0 {
		separator = w.cmp.Successor(w.comparerScratch[:0], w.dataBlock.prevKey)
	} else {
		separator = w.cmp.Separator(w.comparerScratch[:0], w.dataBlock.prevKey, key)
	}
	if separator == nil {
		separator = w.dataBlock.prevKey
	} else {
		w.comparerScratch = separator
	}
	n := encodeBlockHandle(w.scratch[:20], w.pendingBH)
	// Append the block handle to the index block.
	if err := w.indexBlock.append(separator, w.scratch[:n]); err != nil {
		return err
	}
	// Reset prev key of the data block.
	w.dataBlock.prevKey = w.dataBlock.prevKey[:0]
	// Clear pending block handle.
	w.pendingBH = blockHandle{}
	return nil
}

func (w *Writer) finishBlock() error {
	if err := w.dataBlock.finish(); err != nil {
		return err
	}
	bh, err := w.writeBlock(&w.dataBlock.buf, w.compression)
	if err != nil {
		return err
	}
	w.pendingBH = bh
	// Reset the data block.
	w.dataBlock.reset()
	// Flush the filter block.
	w.filterBlock.flush(w.offset)
	return nil
}

// Append appends key/value pair to the table. The keys passed must
// be in increasing order.
//
// It is safe to modify the contents of the arguments after Append returns.
func (w *Writer) Append(key, value []byte) error {
	if w.err != nil {
		return w.err
	}
	if w.nEntries > 0 && w.cmp.Compare(w.dataBlock.prevKey, key) >= 0 {
		w.err = fmt.Errorf("leveldb/table: Writer: keys are not in increasing order: %q, %q", w.dataBlock.prevKey, key)
		return w.err
	}

	if err := w.flushPendingBH(key); err != nil {
		return err
	}
	// Append key/value pair to the data block.
	if err := w.dataBlock.append(key, value); err != nil {
		return err
	}
	// Add key to the filter block.
	w.filterBlock.add(key)

	// Finish the data block if block size target reached.
	if w.dataBlock.bytesLen() >= w.blockSize {
		if err := w.finishBlock(); err != nil {
			w.err = err
			return w.err
		}
	}
	w.nEntries++
	return nil
}

// BlocksLen returns number of blocks written so far.
func (w *Writer) BlocksLen() int {
	n := w.indexBlock.nEntries
	if w.pendingBH.length > 0 {
		// Includes the pending block.
		n++
	}
	return n
}

// EntriesLen returns number of entries added so far.
func (w *Writer) EntriesLen() int {
	return w.nEntries
}

// BytesLen returns number of bytes written so far.
func (w *Writer) BytesLen() int {
	return int(w.offset)
}

// Close will finalize the table. Calling Append is not possible
// after Close, but calling BlocksLen, EntriesLen and BytesLen
// is still possible.
func (w *Writer) Close() error {
	if w.err != nil {
		if w.bufs != nil {
			w.release()
		}
		return w.err
	}
	defer w.release()

	// Write the last data block. Or empty data block if there
	// aren't any data blocks at all.
	if w.dataBlock.nEntries > 0 || w.nEntries == 0 {
		if err := w.finishBlock(); err != nil {
			w.err = err
			return w.err
		}
	}
	if err := w.flushPendingBH(nil); err != nil {
		return err
	}

	// Write the filter block.
	var filterBH blockHandle
	if err := w.filterBlock.finish(); err != nil {
		return err
	}
	if buf := &w.filterBlock.buf; buf.Len() > 0 {
		filterBH, w.err = w.writeBlock(buf, opt.NoCompression)
		if w.err != nil {
			return w.err
		}
	}

	// Write the metaindex block.
	if filterBH.length > 0 {
		key := []byte("filter." + w.filter.Name())
		n := encodeBlockHandle(w.scratch[:20], filterBH)
		if err := w.dataBlock.append(key, w.scratch[:n]); err != nil {
			return err
		}
	}
	if err := w.dataBlock.finish(); err != nil {
		return err
	}
	metaindexBH, err := w.writeBlock(&w.dataBlock.buf, w.compression)
	if err != nil {
		w.err = err
		return w.err
	}

	// Write the index block.
	if err := w.indexBlock.finish(); err != nil {
		return err
	}
	indexBH, err := w.writeBlock(&w.indexBlock.buf, w.compression)
	if err != nil {
		w.err = err
		return w.err
	}

	// Write the table footer.
	footer := w.scratch[:footerLen]
	for i := range footer {
		footer[i] = 0
	}
	n := encodeBlockHandle(footer, metaindexBH)
	encodeBlockHandle(footer[n:], indexBH)
	copy(footer[footerLen-len(magic):], magic)
	if _, err := w.writer.Write(footer); err != nil {
		w.err = err
		return w.err
	}
	w.offset += footerLen

	w.err = errWriterClosed
	return nil
}

// NewWriter creates a new initialized table writer for the file.
//
// Table writer is not safe for concurrent use.
func NewWriter(f io.Writer, o *opt.Options, pool *util.BufferPool, size int) *Writer {
	var bufBytes []byte
	if pool == nil {
		bufBytes = make([]byte, size)
	} else {
		bufBytes = pool.Get(size)
	}
	bufBytes = bufBytes[:0]

	b := writerBufsPool.Get().(*writerBufs)
	w := &Writer{
		writer:             f,
		cmp:                o.GetComparer(),
		filter:             o.GetFilter(),
		compression:        o.GetCompression(),
		blockSize:          o.GetBlockSize(),
		comparerScratch:    make([]byte, 0),
		compressionScratch: b.compression,
		bpool:              pool,
		bufs:               b,
		dataBlock:          blockWriter{buf: *util.NewBuffer(bufBytes)},
	}
	// data block
	w.dataBlock.restartInterval = o.GetBlockRestartInterval()
	// The first 20-bytes are used for encoding block handle.
	w.dataBlock.scratch = w.scratch[20:]
	w.dataBlock.prevKey = b.dataPrevKey
	w.dataBlock.restarts = b.dataRestarts
	// index block
	w.indexBlock.restartInterval = 1
	w.indexBlock.scratch = w.scratch[20:]
	w.indexBlock.buf = *util.NewBuffer(b.index)
	w.indexBlock.prevKey = b.indexKey
	w.indexBlock.restarts = b.indexRestarts
	// filter block
	w.filterBlock.buf = *util.NewBuffer(b.filter)
	w.filterBlock.offsets = b.filterOffsets
	if w.filter != nil {
		w.filterBlock.generator = w.filter.NewGenerator()
		w.filterBlock.baseLg = uint(o.GetFilterBaseLg())
		w.filterBlock.flush(0)
	}
	return w
}
