package websocket

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"emperror.dev/errors"
	"github.com/banzaicloud/kurun/tunnel"
	logrtesting "github.com/go-logr/logr/testing"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestTunnel(t *testing.T) {
	tunnelServer := NewServer(WithLogger(logrtesting.NewTestLogger(t)))
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	reqCtx, cancelReq := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelReq()

	origReq, err := http.NewRequestWithContext(reqCtx, "MyCustomVerb", "MyCustomScheme://MyCustomHost/My/Custom/Path?MyCustom=Query#MyCustomHash", strings.NewReader("MyCustomBody"))
	require.NoError(t, err)
	origResp := &http.Response{
		StatusCode: 666,
		Proto:      origReq.Proto,
		ProtoMajor: origReq.ProtoMajor,
		ProtoMinor: origReq.ProtoMinor,
		Body:       NopReadSeekCloser(strings.NewReader("MyCustomResponseBody")),
	}
	origResp.Status = http.StatusText(origResp.StatusCode)

	tunnelClientCfg := NewClientConfig("ws://"+tunnelControlServer.Addr, tunnel.RoundTripperFunc(func(recvReq *http.Request) (*http.Response, error) {
		compareRequests(t, origReq, recvReq)
		return origResp, nil
	}))

	clientCtx, stopClient := context.WithCancel(context.Background())
	defer stopClient()
	go func() {
		require.NoError(t, RunClient(clientCtx, *tunnelClientCfg))
	}()

	recvResp, err := tunnelServer.RoundTrip(origReq)
	require.NoError(t, err)
	compareResponses(t, origResp, recvResp)
}

func TestTunnelConcurrency(t *testing.T) {
	tunnelServer := NewServer(WithLogger(logrtesting.NewTestLogger(t)))
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	cc := NewCounter()
	tunnelClientCfg := NewClientConfig("ws://"+tunnelControlServer.Addr, tunnel.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		cc.Inc()
		defer cc.Dec()

		time.Sleep(100 * time.Millisecond)
		return pathEcho(req)
	}))

	clientCtx, stopClient := context.WithCancel(context.Background())
	defer stopClient()
	go func() {
		require.NoError(t, RunClient(clientCtx, *tunnelClientCfg))
	}()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		i := i
		go func() {
			value := fmt.Sprintf("/my/custom/path/%d", i)
			req, err := http.NewRequest(http.MethodGet, value, nil)
			require.NoError(t, err)
			resp, err := tunnelServer.RoundTrip(req)
			require.NoError(t, err)

			dat, err := ioutil.ReadAll(resp.Body)
			resp.Body.Close()
			require.NoError(t, err)
			require.Equal(t, value, string(dat))

			wg.Done()
		}()
	}
	wg.Wait()
	require.Greater(t, cc.Max(), 1)
	require.Equal(t, cc.Min(), 0)
}

func TestTunnelTLS(t *testing.T) {
	serverTLSCfg, clientTLSCfg := generateTLSConfigs(t)

	tunnelServer := NewServer(WithLogger(logrtesting.NewTestLogger(t)))
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:      "localhost:10080",
		Handler:   tunnelServer,
		TLSConfig: serverTLSCfg.Clone(),
	}
	go tunnelControlServer.ListenAndServeTLS("", "")
	defer tunnelControlServer.Shutdown(context.Background())

	tunnelClientCfg := NewClientConfig("wss://"+tunnelControlServer.Addr, tunnel.RoundTripperFunc(pathEcho), WithDialerCtor(func() *websocket.Dialer {
		return &websocket.Dialer{
			HandshakeTimeout: websocket.DefaultDialer.HandshakeTimeout,
			Proxy:            websocket.DefaultDialer.Proxy,
			TLSClientConfig:  clientTLSCfg.Clone(),
		}
	}))

	clientCtx, stopClient := context.WithCancel(context.Background())
	defer stopClient()
	go func() {
		require.NoError(t, RunClient(clientCtx, *tunnelClientCfg))
	}()

	reqCtx, cancelReq := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelReq()

	value := "/my/custom/path"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, value, nil)
	require.NoError(t, err)
	resp, err := tunnelServer.RoundTrip(req)
	require.NoError(t, err)

	dat, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, value, string(dat))
}

func TestRequestHandlingWithoutWebsocketConnection(t *testing.T) {
	tunnelServer := NewServer(WithLogger(logrtesting.NewTestLogger(t)))
	require.NotNil(t, tunnelServer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	value := "/my/custom/path"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	require.NoError(t, err)
	resp, err := tunnelServer.RoundTrip(req)
	require.Nil(t, resp)
	err = errors.Cause(err)
	require.Error(t, err)
	require.Contains(t, err.Error(), context.DeadlineExceeded.Error()) // because http.httpError stores its cause as string...
	require.Equal(t, context.DeadlineExceeded, ctx.Err())
}

func TestProxiedErrorMessage(t *testing.T) {
	tunnelServer := NewServer(WithLogger(logrtesting.NewTestLogger(t)))
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	errMsg := "my custom error message"
	tunnelClientCfg := NewClientConfig("ws://"+tunnelControlServer.Addr, tunnel.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.NewPlain(errMsg)
	}))

	clientCtx, stopClient := context.WithCancel(context.Background())
	defer stopClient()
	go func() {
		require.NoError(t, RunClient(clientCtx, *tunnelClientCfg))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	value := "/my/custom/path"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	require.NoError(t, err)
	resp, err := tunnelServer.RoundTrip(req)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.NoError(t, err)

	dat, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, errMsg, string(dat))
}

func TestConnectionSwitch(t *testing.T) {
	tunnelServer := NewServer(WithLogger(logrtesting.NewTestLogger(t)))
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	client1Ctx, stopClient1 := context.WithCancel(context.Background())
	go func() {
		require.NoError(t, RunClient(client1Ctx, *NewClientConfig("ws://"+tunnelControlServer.Addr, tunnel.RoundTripperFunc(staticResp([]byte("client1"))))))
	}()
	time.Sleep(1 * time.Second)
	stopClient1()

	client2Ctx, stopClient2 := context.WithCancel(context.Background())
	defer stopClient2()
	go func() {
		require.NoError(t, RunClient(client2Ctx, *NewClientConfig("ws://"+tunnelControlServer.Addr, tunnel.RoundTripperFunc(staticResp([]byte("client2"))))))
	}()
	time.Sleep(1 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	require.NoError(t, err)
	resp, err := tunnelServer.RoundTrip(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "client2", string(body))
}

func TestTunnelBigResponse(t *testing.T) {
	tunnelServer := NewServer(WithLogger(logrtesting.NewTestLogger(t)))
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	data := make([]byte, 50*1024*1024)
	for i := range data {
		data[i] = byte(rand.Intn(256))
	}

	tunnelClientCfg := NewClientConfig("ws://"+tunnelControlServer.Addr, tunnel.RoundTripperFunc(staticResp(data)))

	clientCtx, stopClient := context.WithCancel(context.Background())
	defer stopClient()
	go func() {
		require.NoError(t, RunClient(clientCtx, *tunnelClientCfg))
	}()

	targetServer := http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write(data)
		}),
	}
	go targetServer.ListenAndServeTLS("", "")
	defer targetServer.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	require.NoError(t, err)
	resp, err := tunnelServer.RoundTrip(req)
	require.NoError(t, err)

	dat, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, data, dat)
}

func pathEcho(req *http.Request) (*http.Response, error) {
	return &http.Response{
		Body: io.NopCloser(strings.NewReader(req.URL.Path)),
	}, nil
}

func staticResp(body []byte) func(req *http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:        http.StatusText(http.StatusOK),
			StatusCode:    http.StatusOK,
			Proto:         req.Proto,
			ProtoMajor:    req.ProtoMajor,
			ProtoMinor:    req.ProtoMinor,
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}, nil
	}
}
