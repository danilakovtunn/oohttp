// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http2

import (
	"errors"
	"io"
	"sync"
)

// pipe is a goroutine-safe io.Reader/io.Writer pair.  It's like
// io.Pipe except there are no PipeReader/PipeWriter halves, and the
// underlying buffer is an interface. (io.Pipe is always unbuffered)
type pipe struct {
	mu     sync.Mutex
	c      sync.Cond // c.L must point to
	b      pipeBuffer
	err    error         // read error once empty. non-nil means closed.
	donec  chan struct{} // closed on error
	readFn func()        // optional code to run in Read before error
}

type pipeBuffer interface {
	Len() int
	io.Writer
	io.Reader
}

// Read waits until data is available and copies bytes
// from the buffer into p.
func (p *pipe) Read(d []byte) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c.L == nil {
		p.c.L = &p.mu
	}
	for {
		if p.b.Len() > 0 {
			return p.b.Read(d)
		}
		if p.err != nil {
			if p.readFn != nil {
				p.readFn()     // e.g. copy trailers
				p.readFn = nil // not sticky like p.err
			}
			return 0, p.err
		}
		p.c.Wait()
	}
}

var errClosedPipeWrite = errors.New("write on closed buffer")

// Write copies bytes from p into the buffer and wakes a reader.
// It is an error to write more data than the buffer can hold.
func (p *pipe) Write(d []byte) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c.L == nil {
		p.c.L = &p.mu
	}
	defer p.c.Signal()
	if p.err != nil {
		return 0, errClosedPipeWrite
	}
	return p.b.Write(d)
}

// CloseWithError causes the next Read (waking up a current blocked
// Read if needed) to return the provided err after all data has been
// read.
//
// The error must be non-nil.
func (p *pipe) CloseWithError(err error) { p.closeWithErrorAndCode(err, nil) }

// closeWithErrorAndCode is like CloseWithError but also sets some code to run
// in the caller's goroutine before returning the error.
func (p *pipe) closeWithErrorAndCode(err error, fn func()) {
	if err == nil {
		panic("CloseWithError err must be non-nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c.L == nil {
		p.c.L = &p.mu
	}
	defer p.c.Signal()
	if p.err != nil {
		// Already been done.
		return
	}
	p.readFn = fn
	p.err = err
	if p.donec != nil {
		close(p.donec)
	}
}

// Err returns the error (if any) first set with CloseWithError.
// This is the error which will be returned after the reader is exhausted.
func (p *pipe) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Done returns a channel which is closed if and when this pipe is closed
// with CloseWithError.
func (p *pipe) Done() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.donec == nil {
		p.donec = make(chan struct{})
		if p.err != nil {
			// Already hit an error.
			close(p.donec)
		}
	}
	return p.donec
}
