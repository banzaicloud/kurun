package websocket

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/banzaicloud/kurun/tunnel"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestTunnel(t *testing.T) {
	server := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, server)
	go http.ListenAndServe("localhost:10080", server)
	go http.ListenAndServe("localhost:10081", tunnel.RequestHandler{RoundTripper: server})

	client := NewClient("ws://localhost:10080", "localhost:10082")
	err := client.Start(context.Background())
	require.NoError(t, err)

	go http.ListenAndServe("localhost:10082", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(r.URL.Path))
	}))

	value := "/alma/korte/maci"
	resp, err := http.Get("http://localhost:10081" + value)
	require.NoError(t, err)
	defer resp.Body.Close()
	dat, err := ioutil.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, value, string(dat))
}

func TestTunnelConcurrency(t *testing.T) {
	server := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, server)
	go http.ListenAndServe("localhost:10080", server)
	go http.ListenAndServe("localhost:10081", tunnel.RequestHandler{RoundTripper: server})

	client := NewClient("ws://localhost:10080", "localhost:10082")
	err := client.Start(context.Background())
	require.NoError(t, err)

	reqCnt := int64(0)
	maxCnt := int64(0)
	var mux sync.Mutex
	go http.ListenAndServe("localhost:10082", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mux.Lock()
		reqCnt++
		if reqCnt > maxCnt {
			maxCnt = reqCnt
		}
		mux.Unlock()
		time.Sleep(100 * time.Millisecond)
		rw.Write([]byte(r.URL.Path))
		atomic.AddInt64(&reqCnt, int64(-1))
	}))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		i := i
		go func() {
			value := fmt.Sprintf("/alma/korte/maci/%d", i)
			resp, err := http.Get("http://localhost:10081" + value)
			require.NoError(t, err)
			defer resp.Body.Close()
			dat, err := ioutil.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, value, string(dat))
			wg.Done()
		}()
	}
	wg.Wait()
	require.Greater(t, maxCnt, int64(1))
}

func TestTunnelTLS(t *testing.T) {
	server := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, server)
	go http.ListenAndServe("localhost:10080", server)

	certFile := "../../localhost+2.pem"
	keyFile := "../../localhost+2-key.pem"
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	require.NoError(t, err)
	caBytes, err := ioutil.ReadFile("../../rootCA.pem")
	require.NoError(t, err)
	certPool := x509.NewCertPool()
	require.True(t, certPool.AppendCertsFromPEM(caBytes))
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
	}
	reqSrv := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.RequestHandler{RoundTripper: server},
		TLSConfig: tlsCfg.Clone(),
	}
	go reqSrv.ListenAndServeTLS("", "")

	clcl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg.Clone(),
		},
	}
	client := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&clcl), WithTLSTarget())
	require.NoError(t, client.Start(context.Background()))

	prxSrv := http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte(r.URL.Path))
		}),
		TLSConfig: tlsCfg.Clone(),
	}
	go prxSrv.ListenAndServeTLS("", "")

	value := "/alma/korte/maci"
	cl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg.Clone(),
		},
	}
	resp, err := cl.Get("https://localhost:10081" + value)
	require.NoError(t, err)
	defer resp.Body.Close()
	dat, err := ioutil.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, value, string(dat))
}
