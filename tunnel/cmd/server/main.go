package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/banzaicloud/kurun/tunnel"
	tunnelws "github.com/banzaicloud/kurun/tunnel/websocket"
	"github.com/gorilla/websocket"
)

type Params struct {
	cert string
	key  string
	ca   string
}

func main() {
	params := Params{}

	flag.StringVar(&params.cert, "cert", "", "cert")
	flag.StringVar(&params.key, "key", "", "key")
	flag.StringVar(&params.ca, "ca", "", "ca")
	flag.Parse()

	if flag.NFlag() < 3 {
		fmt.Printf("%+v\n", params)
		flag.Usage()
		return
	}

	cert, err := tls.LoadX509KeyPair(params.cert, params.key)
	if err != nil {
		panic(err)
	}
	caBytes, err := ioutil.ReadFile(params.ca)
	if err != nil {
		panic(err)
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caBytes) {
		panic("ca certpool append error")
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
	}

	server := tunnelws.NewServer(
		tunnelws.WithUpgrader(websocket.Upgrader{
			CheckOrigin:      func(r *http.Request) bool { return true },
			HandshakeTimeout: 15 * time.Second,
		}),
	)

	srv := http.Server{
		Addr:      ":443",
		Handler:   tunnel.RequestHandler{RoundTripper: server},
		TLSConfig: tlsCfg.Clone(),
	}

	wsSrv := &http.Server{Addr: ":80", Handler: server}
	go wsSrv.ListenAndServe()

	srv.ListenAndServeTLS("", "")
}
