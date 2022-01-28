package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"

	tunnelws "github.com/banzaicloud/kurun/tunnel/websocket"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	restCfg := config.GetConfigOrDie()
	tlsCfg, err := rest.TLSConfigFor(restCfg)
	if err != nil {
		panic(err)
	}

	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		panic(err)
	}

	var secret v1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "tunnel-secret"}, &secret); err != nil {
		panic(err)
	}

	caBytes, ok := secret.Data["ca.crt"]
	if !ok {
		panic("no ca cert found in secret")
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caBytes) {
		panic("unable to append CA")
	}
	tlsConfig := &tls.Config{
		RootCAs: certPool,
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	// httpClient := &http.Client{}
	// uri := "ws://localhost:80"

	uri := fmt.Sprintf("%v/%v:%v/proxy/%v", restCfg.Host, "api/v1/namespaces/default/pods/https:tunnel", 80, "")
	uri = strings.Replace(uri, "https://", "wss://", 1)
	fmt.Println("proxy url:", uri)

	// tunnelClient := tunnelws.NewClient(uri, "localhost:8000", tunnelws.WithHTTPClient(httpClient))
	tunnelClient := tunnelws.NewClient(uri, "localhost:8000", tunnelws.WithHTTPClient(httpClient), tunnelws.WithTLSTarget(), tunnelws.WithServerTLS(tlsCfg))
	if err := tunnelClient.Start(context.Background()); err != nil {
		fmt.Println(err)
		cancel()
	}

	<-ctx.Done()
}
