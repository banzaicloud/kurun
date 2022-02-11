package websocket

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"sync"

	"emperror.dev/errors"
	"github.com/banzaicloud/kurun/tunnel/pkg/workplace"
	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"
)

// TODO: add metrics

// NewServer returns a new Server instance
func NewServer(options ...ServerOption) *Server {
	s := &Server{
		logger:    logr.Discard(),
		requestCh: make(chan *http.Request),
		stopCh:    make(chan struct{}),
		waitQueue: waitQueue{
			items: make(map[requestID]waitQueueItem),
		},
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option.ApplyToServer(s)
	}
	return s
}

// Server implements a tunnel server using WebSockets
type Server struct {
	upgrader websocket.Upgrader
	logger   logr.Logger

	requestCh chan *http.Request
	stopCh    chan struct{}
	waitQueue waitQueue
}

// RoundTrip sends the request through the tunnel and returns the response
func (s *Server) RoundTrip(req *http.Request) (*http.Response, error) {
	s.logger.V(1).Info("request received", "request", req)

	if s.stopped() {
		return nil, errors.New("tunnel server stopped")
	}

	if err := req.Context().Err(); err != nil {
		return nil, err
	}

	respCh := s.queueRequest(req)

	if respCh == nil {
		return nil, errors.New("no response channel")
	}

	select {
	case respAndErr, ok := <-respCh:
		if !ok {
			return nil, errors.New("response channel closed")
		}
		return respAndErr.resp, respAndErr.err
	case <-s.stopCh: // this branch allows shutdown by the server supervisor
		s.cancelRequest(req)
		return nil, errors.New("tunnel server stopped")
	case <-req.Context().Done(): // this branch allows cancellation and timeout by the requester
		s.cancelRequest(req)
		return nil, req.Context().Err()
	}
}

// ServeHTTP serves server control requests (e.g. connection)
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// NOTE: Currently, only connection requests are handled here, but
	//       any new control requests (e.g. remote shutdown, pprof, metrics)
	//       should be handled here as well

	s.logger.Info("connection received", "request", r)

	wsConn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error(err, "failed to upgrade connection")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.logger.V(1).Info("connection successfully upgraded")

	c := &conn{
		requestCh: s.requestCh,
		waitQueue: &s.waitQueue,
		wsConn:    wsConn,
	}
	c.logger = s.logger.WithValues("conn", c)
	go c.run(s.stopCh)
}

// Shutdown initiates server shutdown, but does not wait for it to finish
func (s *Server) Shutdown() {
	s.logger.Info("initiating websocket tunnel server shutdown")
	close(s.stopCh)
}

// cancelRequest drops the specified request from the wait queue
func (s *Server) cancelRequest(req *http.Request) {
	s.logger.V(1).Info("request cancelled", "request", req)
	s.waitQueue.dropItem(getRequestID(req))
}

// queueRequest registers the request in the wait queue and return a channel to wait on for the response
func (s *Server) queueRequest(req *http.Request) <-chan responseAndError {
	id := getRequestID(req)

	logger := s.logger.WithValues("request", req, "id", id)

	ch := make(chan responseAndError, 1)
	item := waitQueueItem{
		req:    req,
		respCh: ch,
	}

	s.waitQueue.pushItem(id, item)
	logger.V(2).Info("item pushed to wait queue", "item", item)

	select {
	case <-s.stopCh:
		s.waitQueue.dropItem(id)
		respondToRequest(logger, item, nil, errors.New("tunnel server stopped"))
	case <-req.Context().Done():
		s.waitQueue.dropItem(id)
		respondToRequest(logger, item, nil, req.Context().Err())
	case s.requestCh <- req:
		logger.V(1).Info("request queued")
	}

	return ch
}

// stopped returns whether server shutdown has been initiated
func (s *Server) stopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

type conn struct {
	logger    logr.Logger
	requestCh chan *http.Request
	waitQueue *waitQueue
	wp        workplace.Workplace
	wsConn    *websocket.Conn
}

// readLoop reads responses from the WebSocket connection
func (c *conn) readLoop() {
	logger := c.logger.WithName("readLoop")
	defer logger.V(1).Info("websocket connection reader loop terminated")

	for {
		if !c.wp.Open() {
			logger.V(1).Info("connection closing, terminating reader loop")
			return
		}

		logger.V(2).Info("getting next reader")
		typ, rdr, err := c.wsConn.NextReader()
		if err != nil {
			if isTemporaryError(err) {
				logger.V(1).Error(err, "got temporary error when getting next reader")
				continue
			}
			if closeError, ok := err.(*websocket.CloseError); ok {
				logger.Info("websocket connection closed", "code", closeError.Code, "text", closeError.Text)
			} else {
				logger.Error(err, "failed to get next reader")
			}
			return
		}
		switch typ {
		case websocket.BinaryMessage:
			var reqID requestID
			if err := binary.Read(rdr, binary.LittleEndian, &reqID); err != nil {
				logger.Error(err, "failed to read request ID")
				continue
			}
			item, found := c.waitQueue.popItem(reqID)
			if !found {
				// either the request has been cancelled, never existed, or we have a bug
				logger.V(1).Info("no wait queue item for request ID", "id", reqID)
				continue
			}
			// read all data before a new reader is created for the connection and the current reader is invalidated
			respBytes, err := io.ReadAll(rdr)
			if err != nil {
				respondToRequest(logger, item, nil, err)
				continue
			}
			resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(respBytes)), item.req)
			respondToRequest(logger, item, resp, err)
		default:
			logger.V(1).Info("ignoring message", "type", typ)
		}
	}
}

// requeueRequest puts the specified request back into the request channel without updating the wait queue
func (c *conn) requeueRequest(req *http.Request) {
	logger := c.logger.WithValues("request", req)
	select {
	case <-c.wp.Closing():
		logger.Info("connection closing, bailing on requeue")
	case c.requestCh <- req:
		logger.V(1).Info("request requeued")
	}
}

func (c *conn) run(stop <-chan struct{}) {
	go c.writeLoop()
	go c.readLoop()

	select {
	case <-stop:
		c.wp.Close(nil)
	case <-c.wp.Closing():
	}
}

func (c *conn) tryCloseConnection(reason string) {
	data := websocket.FormatCloseMessage(websocket.CloseGoingAway, reason)
	if err := c.wsConn.WriteMessage(websocket.CloseMessage, data); err != nil {
		c.logger.Error(err, "failed to write close message to websocket connection")
	}
}

// writeLoop writes incoming requests to the WebSocket connection
func (c *conn) writeLoop() {
	logger := c.logger.WithName("writeLoop")
	defer logger.V(1).Info("websocket connection writer loop terminated")

	defer c.tryCloseConnection("tunnel server terminating")

	for {
		select {
		case <-c.wp.Closing():
			return
		case req := <-c.requestCh:
			logger.V(1).Info("processing request", "request", req)

			wc, err := c.wsConn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				go c.requeueRequest(req)
				if isTemporaryError(err) {
					logger.V(1).Error(err, "got temporary error when getting next writer")
					continue
				}
				logger.Error(err, "failed to get next writer")
				return
			}
			if err := writeRequestAndClose(wc, req); err != nil {
				go c.requeueRequest(req)
				if isTemporaryError(err) {
					logger.V(1).Error(err, "got temporary error when writing request to websocket connection")
					continue
				}
				logger.Error(err, "failed to write request to websocket connection", "request", req)
				return
			}
		}
	}
}

type ServerOption interface {
	ApplyToServer(*Server)
}

type ServerOptionFunc func(*Server)

func (opt ServerOptionFunc) ApplyToServer(s *Server) {
	opt(s)
}

func WithUpgrader(upgrader websocket.Upgrader) ServerOption {
	return ServerOptionFunc(func(s *Server) {
		s.upgrader = upgrader
	})
}

// respondToRequest responds to the request in item using the response channel in item and closes the channel
func respondToRequest(logger logr.Logger, item waitQueueItem, resp *http.Response, err error) {
	if item.respCh == nil {
		logger.Info("response channel is nil for request", "request", item.req)
		return
	}
	item.respCh <- responseAndError{
		resp: resp,
		err:  err,
	}
	close(item.respCh)
}

type waitQueue struct {
	items map[requestID]waitQueueItem
	mutex sync.Mutex
}

func (q *waitQueue) dropItem(id requestID) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	delete(q.items, id)
}

func (q *waitQueue) popItem(id requestID) (item waitQueueItem, found bool) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	item, found = q.items[id]
	if found {
		delete(q.items, id)
	}
	return
}

func (q *waitQueue) pushItem(id requestID, item waitQueueItem) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.items[id] = item
}

type waitQueueItem struct {
	req    *http.Request
	respCh chan<- responseAndError
}

type responseAndError struct {
	resp *http.Response
	err  error
}

func writeRequestAndClose(w io.WriteCloser, r *http.Request) error {
	defer w.Close()
	if err := binary.Write(w, binary.LittleEndian, getRequestID(r)); err != nil {
		return err
	}
	return r.Write(w)
}
