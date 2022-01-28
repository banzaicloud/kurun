//usr/local/go/bin/go run $0 "$@"; exit $?;
package main

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

type Params struct {
	cert string
	key  string
	ca   string
}

func main() {
	restCfg := config.GetConfigOrDie()

	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		panic(err)
	}

	var secret v1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "tunnel-secret"}, &secret); err != nil {
		panic(err)
	}

	certBytes, ok := secret.Data["tls.crt"]
	if !ok {
		panic("no cert found in secret")
	}
	keyBytes, ok := secret.Data["tls.key"]
	if !ok {
		panic("no key cert found in secret")
	}

	tlsCert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		panic(err)
	}

	httpServer := http.Server{
		Addr: ":8000",
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Println("request received:", r.URL)
			http.FileServer(http.Dir(".")).ServeHTTP(w, r)
		}),
	}

	log.Fatal(httpServer.ListenAndServeTLS("", ""))

}
