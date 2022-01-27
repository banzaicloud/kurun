package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"

	tunnelws "github.com/banzaicloud/kurun/tunnel/websocket"
)

const (
	CertFile = "../../localhost+2.pem"
	KeyFile  = "../../localhost+2-key.pem"
	CAFile   = "../../rootCA.pem"
)

func loadTLSConfig(certFile string, keyFile string, CAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caBytes, err := ioutil.ReadFile(CAFile)
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("unable to append CA")
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
	}
	return tlsCfg, nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	// tlsConfig, err := loadTLSConfig(CertFile, KeyFile, CAFile)
	// if err != nil {
	// 	panic(err)
	// }
	// httpClient := &http.Client{
	// 	Transport: &http.Transport{
	// 		TLSClientConfig: tlsConfig,
	// 	},
	// }

	httpClient := &http.Client{}

	client := tunnelws.NewClient("ws://localhost:10080", "localhost:8000", tunnelws.WithHTTPClient(httpClient))
	// client := tunnelws.NewClient("ws://localhost:10080", "localhost:8000", tunnelws.WithHTTPClient(httpClient), tunnelws.WithTLSTarget())
	if err := client.Start(context.Background()); err != nil {
		fmt.Println(err)
		cancel()
	}

	<-ctx.Done()
}
