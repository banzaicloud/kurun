package websocket

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
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
	wsSrv := &http.Server{Addr: "localhost:10080", Handler: server}
	go wsSrv.ListenAndServe()
	defer wsSrv.Shutdown(context.Background())
	srv := &http.Server{Addr: "localhost:10081", Handler: tunnel.RequestHandler{RoundTripper: server}}
	go srv.ListenAndServe()
	defer srv.Shutdown(context.Background())

	client := NewClient("ws://localhost:10080", "localhost:10082")
	err := client.Start(context.Background())
	require.NoError(t, err)

	bck := &http.Server{Addr: "localhost:10082", Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(r.URL.Path))
	})}
	go bck.ListenAndServe()
	defer bck.Shutdown(context.Background())

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
	wsSrv := &http.Server{Addr: "localhost:10080", Handler: server}
	go wsSrv.ListenAndServe()
	defer wsSrv.Shutdown(context.Background())
	srv := &http.Server{Addr: "localhost:10081", Handler: tunnel.RequestHandler{RoundTripper: server}}
	go srv.ListenAndServe()
	defer srv.Shutdown(context.Background())

	client := NewClient("ws://localhost:10080", "localhost:10082")
	err := client.Start(context.Background())
	require.NoError(t, err)

	cc := NewCounter()
	bck := &http.Server{Addr: "localhost:10082", Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		cc.Inc()
		time.Sleep(100 * time.Millisecond)
		rw.Write([]byte(r.URL.Path))
		cc.Dec()
	})}
	go bck.ListenAndServe()
	defer bck.Shutdown(context.Background())

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
	require.Greater(t, cc.Max(), 1)
	require.Equal(t, cc.Min(), 0)
}

func TestTunnelTLS(t *testing.T) {
	server := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, server)
	wsSrv := &http.Server{Addr: "localhost:10080", Handler: server}
	go wsSrv.ListenAndServe()
	defer wsSrv.Shutdown(context.Background())

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
	srv := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.RequestHandler{RoundTripper: server},
		TLSConfig: tlsCfg.Clone(),
	}
	go srv.ListenAndServeTLS("", "")
	defer srv.Shutdown(context.Background())

	clcl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg.Clone(),
		},
	}
	client := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&clcl), WithTLSTarget())
	require.NoError(t, client.Start(context.Background()))

	bck := http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte(r.URL.Path))
		}),
		TLSConfig: tlsCfg.Clone(),
	}
	go bck.ListenAndServeTLS("", "")
	defer bck.Shutdown(context.Background())

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
