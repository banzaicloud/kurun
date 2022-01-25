package websocket

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"sync"
	"unsafe"

	"github.com/gorilla/websocket"
)

func NewServer(options ...ServerOption) *Server {
	s := &Server{
		respQueue:   make(map[requestID]responseQueueItem),
		requestChan: make(chan *http.Request),
		stop:        make(chan struct{}),
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(s)
	}
	return s
}

type ServerOption func(*Server)

func WithUpgrader(upgrader websocket.Upgrader) ServerOption {
	return ServerOption(func(s *Server) {
		s.upgrader = upgrader
	})
}

type Server struct {
	upgrader     websocket.Upgrader
	wsConn       *websocket.Conn
	requestChan  chan *http.Request
	respQueue    map[requestID]responseQueueItem
	respQueueMux sync.Mutex
	stop         chan struct{}
}

func (s *Server) RoundTrip(req *http.Request) (*http.Response, error) {
	respCh := s.queueRequest(req)

	select {
	case respAndErr, ok := <-respCh:
		if !ok {
			return nil, net.ErrClosed
		}
		return respAndErr.resp, respAndErr.err
	case <-req.Context().Done():
		return nil, nil
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.wsConn != nil {
		return // TODO: how should we handle further connection attempts
	}

	var err error
	s.wsConn, err = s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	go s.processRequests()
	go s.readWebSocket()
}

func (s *Server) Shutdown() {
	close(s.stop)
	if wsConn := s.wsConn; wsConn != nil {
		if err := wsConn.Close(); err != nil {
			// TODO: log error
		}
	}
}

func (s *Server) queueRequest(req *http.Request) <-chan responseAndError {
	id := getRequestID(req)
	ch := make(chan responseAndError)
	item := responseQueueItem{
		req:    req,
		respCh: ch,
	}
	s.respQueueMux.Lock()
	s.respQueue[id] = item
	s.respQueueMux.Unlock()
	select {
	case s.requestChan <- req:
	case <-s.stop:
		s.respQueueMux.Lock()
		delete(s.respQueue, id)
		s.respQueueMux.Unlock()
		close(ch)
	}
	return ch
}

func (s *Server) processRequests() {
	for {
		select {
		case <-s.stop:
			return
		case req, ok := <-s.requestChan:
			if !ok {
				return
			}
			wc, err := s.wsConn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				s.respondToRequest(req, nil, err)
				continue
			}
			if err := writeRequestAndClose(wc, req); err != nil {
				s.respondToRequest(req, nil, err)
				continue
			}
		}
	}
}

func (s *Server) respondToRequest(req *http.Request, resp *http.Response, err error) {
	reqID := getRequestID(req)

	s.respQueueMux.Lock()
	respItem := s.respQueue[reqID]
	delete(s.respQueue, reqID)
	s.respQueueMux.Unlock()

	if respItem.respCh == nil {
		// TODO: log missing response channel
		return
	}
	respItem.respCh <- responseAndError{
		resp: resp,
		err:  err,
	}
}

func (s *Server) readWebSocket() {
	for {
		typ, rdr, err := s.wsConn.NextReader()
		if err != nil {
			if isTemporaryError(err) {
				continue
			}
			// TODO: log error
			return
		}
		switch typ {
		case websocket.BinaryMessage:
			var reqID requestID
			if err := binary.Read(rdr, binary.LittleEndian, &reqID); err != nil {
				// TODO: log error
				continue
			}
			s.respQueueMux.Lock()
			respItem, ok := s.respQueue[reqID]
			s.respQueueMux.Unlock()
			if !ok {
				// TODO: log missing response
				continue
			}
			resp, err := http.ReadResponse(bufio.NewReader(rdr), respItem.req)
			s.respondToRequest(respItem.req, resp, err)
		case websocket.CloseMessage:
			close(s.stop)
			return
		default:
			// ignore message
		}
	}
}

type responseQueueItem struct {
	req    *http.Request
	respCh chan<- responseAndError
}

type responseAndError struct {
	resp *http.Response
	err  error
}

type requestID = uint64

func getRequestID(r *http.Request) requestID {
	return uint64(uintptr(unsafe.Pointer(r)))
}

func writeRequestAndClose(w io.WriteCloser, r *http.Request) error {
	defer w.Close()
	if err := binary.Write(w, binary.LittleEndian, getRequestID(r)); err != nil {
		return err
	}
	return r.Write(w)
}
