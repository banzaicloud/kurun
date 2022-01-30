package websocket

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/banzaicloud/kurun/tunnel"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestTunnel(t *testing.T) {
	tunnelServer := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	tunnelRequestServer := &http.Server{
		Addr:    "localhost:10081",
		Handler: tunnel.NewRequestHandler(tunnelServer),
	}
	go tunnelRequestServer.ListenAndServe()
	defer tunnelRequestServer.Shutdown(context.Background())

	tunnelClient := NewClient("ws://localhost:10080", "localhost:10082")
	err := tunnelClient.Start(context.Background())
	require.NoError(t, err)

	targetServer := &http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte(r.URL.Path))
		}),
	}
	go targetServer.ListenAndServe()
	defer targetServer.Shutdown(context.Background())

	value := "/alma/korte/maci"
	resp, err := http.Get("http://localhost:10081" + value)
	require.NoError(t, err)

	dat, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, value, string(dat))
}

func TestTunnelConcurrency(t *testing.T) {
	tunnelServer := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	tunnelRequestServer := &http.Server{
		Addr:    "localhost:10081",
		Handler: tunnel.NewRequestHandler(tunnelServer),
	}
	go tunnelRequestServer.ListenAndServe()
	defer tunnelRequestServer.Shutdown(context.Background())

	tunnelClient := NewClient("ws://localhost:10080", "localhost:10082")
	err := tunnelClient.Start(context.Background())
	require.NoError(t, err)

	cc := NewCounter()

	targetServer := &http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			cc.Inc()
			time.Sleep(100 * time.Millisecond)
			rw.Write([]byte(r.URL.Path))
			cc.Dec()
		}),
	}
	go targetServer.ListenAndServe()
	defer targetServer.Shutdown(context.Background())

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		i := i
		go func() {
			value := fmt.Sprintf("/alma/korte/maci/%d", i)
			resp, err := http.Get("http://localhost:10081" + value)
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

	tunnelServer := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	tunnelRequestServer := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.NewRequestHandler(tunnelServer),
		TLSConfig: serverTLSCfg.Clone(),
	}
	go tunnelRequestServer.ListenAndServeTLS("", "")
	defer tunnelRequestServer.Shutdown(context.Background())

	targetClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg.Clone(),
		},
	}
	tunnelClient := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&targetClient), WithTLSTarget())
	err := tunnelClient.Start(context.Background())
	require.NoError(t, err)

	targetServer := http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte(r.URL.Path))
		}),
		TLSConfig: serverTLSCfg.Clone(),
	}
	go targetServer.ListenAndServeTLS("", "")
	defer targetServer.Shutdown(context.Background())

	requestClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg.Clone(),
		},
	}
	value := "/alma/korte/maci"
	resp, err := requestClient.Get("https://localhost:10081" + value)
	require.NoError(t, err)

	dat, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, value, string(dat))
}

func TestRequestHandlingWithoutWebsocketConnection(t *testing.T) {
	serverTLSCfg, clientTLSCfg := generateTLSConfigs(t)

	tunnelServer := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, tunnelServer)

	tunnelRequestServer := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.NewRequestHandler(tunnelServer),
		TLSConfig: serverTLSCfg.Clone(),
	}
	go tunnelRequestServer.ListenAndServeTLS("", "")
	defer tunnelRequestServer.Shutdown(context.Background())

	requestClient := http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg.Clone(),
		},
	}
	value := "/alma/korte/maci"
	resp, err := requestClient.Get("https://localhost:10081" + value)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestProxiedErrorMessage(t *testing.T) {
	serverTLSCfg, clientTLSCfg := generateTLSConfigs(t)

	tunnelServer := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	tunnelRequestServer := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.NewRequestHandler(tunnelServer),
		TLSConfig: serverTLSCfg.Clone(),
	}
	go tunnelRequestServer.ListenAndServeTLS("", "")
	defer tunnelRequestServer.Shutdown(context.Background())

	targetClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg.Clone(),
		},
	}
	tunnelClient := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&targetClient), WithTLSTarget())
	err := tunnelClient.Start(context.Background())
	require.NoError(t, err)

	requestClient := http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg.Clone(),
		},
	}
	value := "/alma/korte/maci"
	resp, err := requestClient.Get("https://localhost:10081" + value)
	require.NoError(t, err)

	dat, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Regexp(t, `^Get "https://localhost:10082/alma/korte/maci": dial tcp .*:10082: connect: connection refused$`, string(dat))
}

func TestMultipleWSConnection(t *testing.T) {
	serverTLSCfg, clientTLSCfg := generateTLSConfigs(t)

	tunnelServer := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	tunnelRequestServer := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.NewRequestHandler(tunnelServer),
		TLSConfig: serverTLSCfg.Clone(),
	}
	go tunnelRequestServer.ListenAndServeTLS("", "")
	defer tunnelRequestServer.Shutdown(context.Background())

	targetClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg.Clone(),
		},
	}
	tunnelClient1 := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&targetClient), WithTLSTarget())
	err := tunnelClient1.Start(context.Background())
	require.NoError(t, err)

	tunnelClient2 := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&targetClient), WithTLSTarget())
	err = tunnelClient2.Start(context.Background())
	require.NoError(t, err)
}

func TestTunnelBigResponse(t *testing.T) {
	serverTLSCfg, clientTLSCfg := generateTLSConfigs(t)

	tunnelServer := NewServer(
		WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)
	require.NotNil(t, tunnelServer)

	tunnelControlServer := &http.Server{
		Addr:    "localhost:10080",
		Handler: tunnelServer,
	}
	go tunnelControlServer.ListenAndServe()
	defer tunnelControlServer.Shutdown(context.Background())

	tunnelRequestServer := http.Server{
		Addr:      "localhost:10081",
		Handler:   tunnel.NewRequestHandler(tunnelServer),
		TLSConfig: serverTLSCfg.Clone(),
	}
	go tunnelRequestServer.ListenAndServeTLS("", "")
	defer tunnelRequestServer.Shutdown(context.Background())

	targetClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg.Clone(),
		},
	}
	tunnelClient := NewClient("ws://localhost:10080", "localhost:10082", WithHTTPClient(&targetClient), WithTLSTarget())
	err := tunnelClient.Start(context.Background())
	require.NoError(t, err)

	data := make([]byte, 50*1024*1024)
	for i := range data {
		data[i] = byte(rand.Intn(256))
	}

	targetServer := http.Server{
		Addr: "localhost:10082",
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write(data)
		}),
		TLSConfig: serverTLSCfg.Clone(),
	}
	go targetServer.ListenAndServeTLS("", "")
	defer targetServer.Shutdown(context.Background())

	requestClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLSCfg.Clone(),
		},
	}
	resp, err := requestClient.Get("https://localhost:10081/")
	require.NoError(t, err)

	dat, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, data, dat)
}
