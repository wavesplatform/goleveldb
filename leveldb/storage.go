package leveldb

import (
	"sync/atomic"

	"github.com/wavesplatform/goleveldb/leveldb/storage"
)

type iStorage struct {
	storage.Storage
	read  atomic.Uint64
	write atomic.Uint64
}

func (c *iStorage) Open(fd storage.FileDesc) (storage.Reader, error) {
	r, err := c.Storage.Open(fd)
	return &iStorageReader{r, c}, err
}

func (c *iStorage) Create(fd storage.FileDesc) (storage.Writer, error) {
	w, err := c.Storage.Create(fd)
	return &iStorageWriter{w, c}, err
}

func (c *iStorage) reads() uint64 {
	return c.read.Load()
}

func (c *iStorage) writes() uint64 {
	return c.write.Load()
}

// newIStorage returns the given storage wrapped by iStorage.
func newIStorage(s storage.Storage) *iStorage {
	return &iStorage{Storage: s}
}

type iStorageReader struct {
	storage.Reader
	c *iStorage
}

func (r *iStorageReader) Read(p []byte) (n int, err error) {
	n, err = r.Reader.Read(p)
	r.c.read.Add(uint64(n))
	return n, err
}

func (r *iStorageReader) ReadAt(p []byte, off int64) (n int, err error) {
	n, err = r.Reader.ReadAt(p, off)
	r.c.read.Add(uint64(n))
	return n, err
}

type iStorageWriter struct {
	storage.Writer
	c *iStorage
}

func (w *iStorageWriter) Write(p []byte) (n int, err error) {
	n, err = w.Writer.Write(p)
	w.c.write.Add(uint64(n))
	return n, err
}
