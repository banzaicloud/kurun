package websocket

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
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
	loadTLSConfig(t)

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

	srv := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.RequestHandler{RoundTripper: server},
		TLSConfig: tlsConfig.Clone(),
	}
	go srv.ListenAndServeTLS("", "")
	defer srv.Shutdown(context.Background())

	clcl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	client := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&clcl), WithTLSTarget())
	require.NoError(t, client.Start(context.Background()))

	bck := http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte(r.URL.Path))
		}),
		TLSConfig: tlsConfig.Clone(),
	}
	go bck.ListenAndServeTLS("", "")
	defer bck.Shutdown(context.Background())

	value := "/alma/korte/maci"
	cl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	resp, err := cl.Get("https://localhost:10081" + value)
	require.NoError(t, err)
	defer resp.Body.Close()
	dat, err := ioutil.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, value, string(dat))
}

func TestRequestHandlingWithoutWebsocketConnection(t *testing.T) {
	loadTLSConfig(t)

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

	srv := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.RequestHandler{RoundTripper: server},
		TLSConfig: tlsConfig.Clone(),
	}
	go srv.ListenAndServeTLS("", "")
	defer srv.Shutdown(context.Background())

	value := "/alma/korte/maci"
	cl := http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	resp, err := cl.Get("https://localhost:10081" + value)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestProxiedErrorMessage(t *testing.T) {
	loadTLSConfig(t)

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

	srv := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.RequestHandler{RoundTripper: server},
		TLSConfig: tlsConfig.Clone(),
	}
	go srv.ListenAndServeTLS("", "")
	defer srv.Shutdown(context.Background())

	clcl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	client := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&clcl), WithTLSTarget())
	require.NoError(t, client.Start(context.Background()))

	value := "/alma/korte/maci"
	cl := http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	resp, err := cl.Get("https://localhost:10081" + value)
	require.NoError(t, err)
	defer resp.Body.Close()
	dat, err := ioutil.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "Get \"https://localhost:10082/alma/korte/maci\": dial tcp [::1]:10082: connect: connection refused", string(dat))
}

func TestMultipleWSConnection(t *testing.T) {
	loadTLSConfig(t)

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

	srv := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.RequestHandler{RoundTripper: server},
		TLSConfig: tlsConfig.Clone(),
	}
	go srv.ListenAndServeTLS("", "")
	defer srv.Shutdown(context.Background())

	clcl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	client := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&clcl), WithTLSTarget())
	require.NoError(t, client.Start(context.Background()))

	clcl2 := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	client2 := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&clcl2), WithTLSTarget())
	require.NoError(t, client2.Start(context.Background()))
}

func TestTunnelBigResponse(t *testing.T) {
	loadTLSConfig(t)

	const datasize = 50 * 1024 * 1024

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

	srv := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.RequestHandler{RoundTripper: server},
		TLSConfig: tlsConfig.Clone(),
	}
	go srv.ListenAndServeTLS("", "")
	defer srv.Shutdown(context.Background())

	clcl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	client := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&clcl), WithTLSTarget())
	require.NoError(t, client.Start(context.Background()))

	bck := http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte(strings.Repeat("A", datasize)))
		}),
		TLSConfig: tlsConfig.Clone(),
	}
	go bck.ListenAndServeTLS("", "")
	defer bck.Shutdown(context.Background())

	cl := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig.Clone(),
		},
	}
	resp, err := cl.Get("https://localhost:10081/")
	require.NoError(t, err)
	defer resp.Body.Close()
	dat, err := ioutil.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, datasize, len(dat))
}
