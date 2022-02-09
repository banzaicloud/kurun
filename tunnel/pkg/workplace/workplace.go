package workplace

import (
	"sync"

	"emperror.dev/errors"
)

// Workplace implements synchronized termination of goroutines.
// Heavily inspired by gopkg.in/tomb.v2, but better.
type Workplace struct {
	closing chan struct{}
	closed  chan struct{}
	err     error
	mutex   sync.Mutex
	once    sync.Once
	workers int
}

func (w *Workplace) Close(err error) {
	w.init()
	if !open(w.closed) {
		return
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.err = errors.Append(w.err, err)
	if open(w.closing) {
		close(w.closing)
	}
}

func (w *Workplace) Closing() <-chan struct{} {
	w.init()
	return w.closing
}

func (w *Workplace) Closed() <-chan struct{} {
	w.init()
	return w.closed
}

func (w *Workplace) Do(fn func()) {
	w.init()
	if !w.start() {
		return
	}
	defer w.done()
	fn()
}

func (w *Workplace) Err() error {
	// no need to init for this
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.err
}

func (w *Workplace) Open() bool {
	w.init()
	return open(w.closing)
}

func (w *Workplace) Wait() error {
	<-w.Closed()
	return w.err // we can read err here without a mutex since it cannot be changed anymore
}

func (w *Workplace) done() {
	// init already run by Do
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.workers--
	if w.workers == 0 && !open(w.closing) && open(w.closed) {
		close(w.closed)
	}
}

func (w *Workplace) init() {
	w.once.Do(func() {
		w.closed = make(chan struct{})
		w.closing = make(chan struct{})
	})
}

func (w *Workplace) start() bool {
	// init already run by Do
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if !open(w.closed) { // workplace is closed, no more work can be started
		return false
	}
	w.workers++
	return true
}

func open(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return false
	default:
		return true
	}
}
