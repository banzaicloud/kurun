package websocket

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"time"

	"emperror.dev/errors"
	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"

	"github.com/banzaicloud/kurun/tunnel/pkg/unwind"
	"github.com/banzaicloud/kurun/tunnel/pkg/workplace"
)

func NewClientConfig(serverAddr string, roundTripper http.RoundTripper, options ...ClientConfigOption) *ClientConfig {
	c := &ClientConfig{
		logger:       logr.Discard(),
		roundTripper: roundTripper,
		serverAddr:   serverAddr,
	}
	for _, opt := range options {
		if opt != nil {
			opt.ApplyToClientConfig(c)
		}
	}
	return c
}

type ClientConfig struct {
	dialerCtor   func() *websocket.Dialer
	logger       logr.Logger
	pingInterval time.Duration
	roundTripper http.RoundTripper
	serverAddr   string
}

type ClientConfigOption interface {
	ApplyToClientConfig(*ClientConfig)
}

type ClientConfigOptionFunc func(cfg *ClientConfig)

func (opt ClientConfigOptionFunc) ApplyToClientConfig(c *ClientConfig) {
	opt(c)
}

func WithDialerCtor(dialerCtor func() *websocket.Dialer) ClientConfigOption {
	return ClientConfigOptionFunc(func(cfg *ClientConfig) {
		cfg.dialerCtor = dialerCtor
	})
}

func RunClient(ctx context.Context, cfg ClientConfig) (err error) {
	dialer := websocket.DefaultDialer
	if dialerCtor := cfg.dialerCtor; dialerCtor != nil {
		dialer = dialerCtor()
	}

	wsConn, _, err := dialer.DialContext(ctx, cfg.serverAddr, nil)
	if err != nil {
		return err
	}

	logger := cfg.logger.WithValues("wsConn", wsConn)

	wsConn.SetPongHandler(func(appData string) error {
		logger.V(1).Info("received pong", "appData", appData)
		return nil
	})

	c := &client{
		responseCh:   make(chan responseItem),
		roundTripper: cfg.roundTripper,
		wsConn:       wsConn,
	}
	c.logger = logger.WithValues("client", c)
	if pingInterval := cfg.pingInterval; pingInterval > 0 {
		c.pingInterval = pingInterval
		c.pingTicker = time.NewTicker(pingInterval)
	}

	go c.wp.Do(func() {
		uc := unwind.WithHandler(func(reason interface{}) {
			c.wp.Close(reasonToError(reason, "in writer loop"))
		})
		if err := uc.DoError(c.writeLoop); err != nil {
			c.wp.Close(err)
		}
	})
	go c.wp.Do(func() {
		uc := unwind.WithHandler(func(reason interface{}) {
			c.wp.Close(reasonToError(reason, "in reader loop"))
		})
		if err := uc.DoError(c.readLoop); err != nil {
			c.wp.Close(err)
		}
	})

	select {
	case <-ctx.Done():
		c.wp.Close(ignoreCancelled(ctx.Err()))
	case <-c.wp.Closing():
	}

	return c.wp.Wait()
}

type client struct {
	logger       logr.Logger
	pingInterval time.Duration
	pingTicker   *time.Ticker
	responseCh   chan responseItem
	roundTripper http.RoundTripper
	wp           workplace.Workplace
	wsConn       *websocket.Conn
}

func (c *client) pingTickerCh() <-chan time.Time {
	if ticker := c.pingTicker; ticker != nil {
		return ticker.C
	}
	return nil
}

func (c *client) handleRequest(reqID requestID, req *http.Request) {
	logger := c.logger.WithValues("id", reqID, "request", req)
	logger.V(1).Info("handling request")

	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	req = req.WithContext(ctx)

	defer triggerWhenClosed(c.wp.Closing(), cancel)() // cancel request if client is closing

	resp, err := c.roundTripper.RoundTrip(req)
	if err != nil {
		logger.Error(err, "round trip failed")

		resp = &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader(err.Error())),
		}
	}

	respItem := responseItem{
		reqID: reqID,
		resp:  resp,
	}
	select {
	case <-c.wp.Closing():
		logger.Info("client closing, bailing on request")
	case c.responseCh <- respItem:
	}
}

func (c *client) readLoop() error {
	logger := c.logger.WithName("readLoop")
	defer logger.V(1).Info("websocket connection reader loop terminated")

	for {
		if !c.wp.Open() {
			logger.V(1).Info("client closing, terminating reader loop")
			return nil
		}

		typ, rdr, err := c.wsConn.NextReader()
		if err != nil {
			if isTemporaryError(err) {
				logger.V(1).Error(err, "got temporary error when getting next reader")
				continue
			}
			if closeError, ok := err.(*websocket.CloseError); ok {
				logger.Info("websocket connection closed by server", "code", closeError.Code, "text", closeError.Text)
				if !c.wp.Open() {
					err = nil // we're already closing
				}
			} else {
				logger.Error(err, "failed to get next reader")
			}
			return err
		}
		c.resetPingTicker()
		switch typ {
		case websocket.BinaryMessage:
			// read all data before a new reader is created for the connection and the current reader is invalidated
			data, err := io.ReadAll(rdr)
			if err != nil {
				logger.Error(err, "failed to read message data")
				continue
			}
			reqID, req, err := readRequest(bytes.NewReader(data))
			if err != nil {
				logger.Error(err, "failed to read request")
				continue
			}
			go c.wp.Do(func() {
				uc := unwind.WithHandler(func(reason interface{}) {
					c.wp.Close(reasonToError(reason, "while handling request"))
				})
				uc.Do(func() {
					c.handleRequest(reqID, req)
				})
			})
		default:
			logger.V(1).Info("ignoring message", "type", typ)
		}
	}
}

func (c *client) requeueResponse(item responseItem) {
	// requeue in a new goroutine to avoid deadlock when requeuing from the writer loop
	go c.wp.Do(func() {
		// note: no important panic can happen here, no need for the unwind-handler-dance
		select {
		case <-c.wp.Closing():
			c.logger.V(1).Info("client closing, bailing on requeue", "id", item.reqID, "response", item.resp)
		case c.responseCh <- item:
			c.logger.V(1).Info("response requeued", "id", item.reqID, "response", item.resp)
		}
	})
}

func (c *client) resetPingTicker() {
	if ticker := c.pingTicker; ticker != nil {
		ticker.Reset(c.pingInterval)
	}
}

func (c *client) tryCloseConnection(reason string) {
	data := websocket.FormatCloseMessage(websocket.CloseGoingAway, reason)
	if err := c.wsConn.WriteMessage(websocket.CloseMessage, data); err != nil {
		c.logger.Error(err, "failed to write close message to websocket connection")
	}
}

func (c *client) stopPingTicker() {
	if ticker := c.pingTicker; ticker != nil {
		ticker.Stop()
	}
}

func (c *client) writeLoop() error {
	logger := c.logger.WithName("writeLoop")
	defer logger.V(1).Info("websocket connection writer loop terminated")

	defer c.tryCloseConnection("tunnel client terminating")
	defer c.stopPingTicker()

	for {
		select {
		case <-c.wp.Closing():
			logger.V(1).Info("client closing, terminating writer loop")
			return nil
		case respItem, ok := <-c.responseCh:
			if !ok {
				err := errors.New("response channel closed")
				logger.Error(err, "unexpected state")
				return err
			}

			logger := logger.WithValues("response", respItem.resp, "id", respItem.reqID)

			wc, err := c.wsConn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				if isTemporaryError(err) {
					logger.V(1).Error(err, "got temporary error when getting next writer")
					c.requeueResponse(respItem)
					continue
				}
				logger.Error(err, "failed to get next writer")
				return err
			}
			if err := writeResponseAndClose(wc, respItem.reqID, respItem.resp); err != nil {
				if isTemporaryError(err) {
					logger.V(1).Error(err, "got temporary error when writing response to websocket connection")
					c.requeueResponse(respItem)
					continue
				}
				logger.Error(err, "failed to write response to websocket connection")
				return err
			}
			c.resetPingTicker()
		case <-c.pingTickerCh():
			logger.V(2).Info("sending ping")
			if err := c.wsConn.WriteMessage(websocket.PingMessage, nil); err != nil {
				if isTemporaryError(err) {
					logger.V(1).Error(err, "got temporary error when sending ping message")
					continue
				}
				logger.Error(err, "failed to send ping message")
				return err
			}
		}
	}
}

type responseItem struct {
	reqID requestID
	resp  *http.Response
}

func readRequest(r io.Reader) (reqID requestID, req *http.Request, err error) {
	if err = binary.Read(r, binary.LittleEndian, &reqID); err != nil {
		return
	}
	req, err = http.ReadRequest(bufio.NewReader(r))
	return
}

func writeResponseAndClose(w io.WriteCloser, reqID requestID, resp *http.Response) error {
	defer w.Close()
	if err := binary.Write(w, binary.LittleEndian, reqID); err != nil {
		return err
	}
	return resp.Write(w)
}

func ignoreCancelled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// triggerWhenClosed triggers the specified action when the specified channel is closed
// The returned function can be used to unarm the trigger
func triggerWhenClosed(ch <-chan struct{}, act func()) (unarm func()) {
	unarmed := make(chan struct{})
	go func() {
		for {
			select {
			case _, open := <-ch:
				if !open {
					act()
					return
				}
			case <-unarmed:
				return
			}
		}
	}()
	return func() {
		close(unarmed)
	}
}
