// This package implements a wrapper around bytes.Buffer which can be subscribed
// to and will only return EOF when the source buffer is closed.
package pubsubbuffer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
)

type WriteSubscribeCloser interface {
	io.WriteCloser
	Len() int
	Subscribe(context.Context) io.Reader
}

// Buffer is a single subscribable buffer.
type Buffer struct {
	buffer bytes.Buffer
	closed bool
	lock   sync.Mutex
	cond   sync.Cond
}

var _ WriteSubscribeCloser = (*Buffer)(nil)
var _ http.Handler = (*Buffer)(nil)

// NewBuffer creates a new empty buffer
func NewBuffer() *Buffer {
	var b Buffer
	b.cond.L = &b.lock
	return &b
}

// Close the buffer. No more writes can occur after this.
func (b *Buffer) Close() error {
	b.lock.Lock()
	defer b.lock.Unlock()
	b.closed = true
	b.cond.Broadcast()
	return nil
}

// Write some data to the buffer.
func (b *Buffer) Write(src []byte) (int, error) {
	b.lock.Lock()
	defer b.lock.Unlock()

	if b.closed {
		return 0, fmt.Errorf("buffer closed")
	}

	n, err := b.buffer.Write(src)
	b.cond.Broadcast()

	return n, err
}

func (b *Buffer) Len() int {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.buffer.Len()
}

// Subscribe to the buffer.
//
// Each subscription will read an entire copy of the buffer
// blocking until Close() is called.
func (b *Buffer) Subscribe(ctx context.Context) io.Reader {
	go func() {
		<-ctx.Done()
		b.lock.Lock()
		defer b.lock.Unlock()
		b.cond.Broadcast()
	}()

	return &reader{
		Buffer:  b,
		Context: ctx,
		offset:  0,
	}
}

// ServeHTTP implements the HTTP protocol for this buffer.
func (b *Buffer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Access-Control-Allow-Origin", "*")
	writer.Header().Set("Access-Control-Expose-Headers", "Transfer-Encoding")

	writer.WriteHeader(http.StatusOK)
	rd := b.Subscribe(request.Context())

	flusher, haveFlusher := writer.(http.Flusher)
	buffer := make([]byte, 4096)

	for {
		n, err := rd.Read(buffer)
		if err != nil {
			break
		}

		_, err = writer.Write(buffer[:n])
		if err != nil {
			break
		}

		if haveFlusher {
			flusher.Flush()
		}
	}
}

type reader struct {
	*Buffer
	context.Context
	offset int
}

var _ io.Reader = (*reader)(nil)

func (b *reader) Read(dest []byte) (int, error) {
	pb := b.Buffer

	pb.lock.Lock()
	defer pb.lock.Unlock()

	for {
		if b.Context.Err() != nil {
			return 0, b.Context.Err()
		}

		by := pb.buffer.Bytes()
		remainder := len(by) - b.offset

		if remainder > 0 {
			toCopy := len(dest)
			if remainder < toCopy {
				toCopy = remainder
			}

			src := by[b.offset:b.offset+toCopy]
			d := dest[:toCopy]
			copy(d, src)
			b.offset += toCopy
			return toCopy, nil
		}

		if pb.closed {
			return 0, io.EOF
		}

		pb.cond.Wait()
	}
}
