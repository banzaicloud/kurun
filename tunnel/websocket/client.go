package websocket

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

func NewClient(serverAddr string, targetHost string, options ...ClientOption) *Client {
	c := &Client{
		serverAddr:   serverAddr,
		targetHost:   targetHost,
		httpClient:   http.DefaultClient,
		pingInterval: 30 * time.Second,
		respCh:       make(chan responseItem),
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(c)
	}
	return c
}

type ClientOption func(*Client)

func WithHTTPClient(httpClient *http.Client) ClientOption {
	return ClientOption(func(c *Client) {
		c.httpClient = httpClient
	})
}

func WithTLSTarget() ClientOption {
	return ClientOption(func(c *Client) {
		c.targetTLS = true
	})
}

type Client struct {
	serverAddr   string
	targetHost   string
	targetTLS    bool
	httpClient   *http.Client
	pingInterval time.Duration
	startOnce    sync.Once
	pingTicker   *time.Ticker
	wsConn       *websocket.Conn
	respCh       chan responseItem
}

type responseItem struct {
	reqID requestID
	resp  *http.Response
}

func (c *Client) Start(ctx context.Context) (err error) {
	c.startOnce.Do(func() {
		err = c.start(ctx)
	})
	return
}

func (c *Client) start(ctx context.Context) error {
	wsConn, _, err := websocket.DefaultDialer.DialContext(ctx, c.serverAddr, nil)
	if err != nil {
		return err
	}

	c.wsConn = wsConn
	c.wsConn.SetPongHandler(func(appData string) error {
		fmt.Println("Pong: ", appData)
		return nil
	})
	c.pingTicker = time.NewTicker(c.pingInterval)

	go c.readWebSocket()
	go c.processResponses(ctx)

	return nil
}

func (c *Client) resetPingTicker() {
	c.pingTicker.Reset(c.pingInterval)
}

func (c *Client) readWebSocket() {
	for {
		typ, rdr, err := c.wsConn.NextReader()
		if err != nil {
			if isTemporaryError(err) {
				continue
			}
			// TODO: log error
			return
		}
		c.resetPingTicker()
		switch typ {
		case websocket.BinaryMessage:
			reqID, req, err := readRequest(rdr)
			if err != nil {
				// TODO: log error
				continue
			}
			go c.handleRequest(reqID, req)
		default:
			// ignore message
		}
	}
}

func (c *Client) handleRequest(reqID requestID, req *http.Request) {
	req.RequestURI = "" // must not be set
	req.URL.Host = c.targetHost
	req.URL.Scheme = "http"
	if c.targetTLS {
		req.URL.Scheme = "https"
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		resp = &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader(err.Error())),
		}
	}
	c.respCh <- responseItem{
		resp:  resp,
		reqID: reqID,
	}
}

func (c *Client) processResponses(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			deadline, _ := ctx.Deadline()
			if err := c.wsConn.WriteControl(websocket.CloseMessage, nil, deadline); err != nil {
				// TODO: log error
			}
			c.pingTicker.Stop()
			return
		case respItem, ok := <-c.respCh:
			if !ok {
				return
			}
			wc, err := c.wsConn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				// TODO: ?
				continue
			}
			if err := writeResponseAndClose(wc, respItem.reqID, respItem.resp); err != nil {
				// TODO: ?
				continue
			}
			c.resetPingTicker()
		case <-c.pingTicker.C:
			if err := c.wsConn.WriteMessage(websocket.PingMessage, nil); err != nil {
				// TODO: log error
				continue
			}
		}
	}
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

func isTemporaryError(err error) bool {
	netErr, ok := err.(net.Error)
	return ok && netErr.Temporary()
}
