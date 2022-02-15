//usr/local/go/bin/go run $0 "$@"; exit $?;
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"

	"github.com/banzaicloud/kurun/tunnel/pkg/tlstools"
	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	var (
		port      int
		tlsSecret string
		useTLS    bool
	)
	pflag.IntVarP(&port, "port", "p", 8000, "port to listen on")
	pflag.StringVar(&tlsSecret, "tlssecret", "", "K8s TLS secret to load certificate from; implies --tls")
	pflag.BoolVar(&useTLS, "tls", false, "listen using TLS")
	pflag.Parse()

	fsServer := http.FileServer(http.Dir("."))
	httpServer := http.Server{
		Addr: fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Println("request received:", r.URL)
			fsServer.ServeHTTP(w, r)
		}),
	}
	if tlsSecret != "" {
		useTLS = true
	}
	if useTLS {
		if httpServer.TLSConfig == nil {
			httpServer.TLSConfig = &tls.Config{}
		}
		if tlsSecret != "" {
			restCfg := config.GetConfigOrDie()

			c, err := client.New(restCfg, client.Options{})
			if err != nil {
				panic(err)
			}

			key := client.ObjectKey{
				Namespace: "default",
				Name:      tlsSecret,
			}
			if parts := strings.SplitN(tlsSecret, string(types.Separator), 2); len(parts) == 2 {
				key.Namespace, key.Name = parts[0], parts[1]
			}
			var secret v1.Secret
			if err := c.Get(context.Background(), key, &secret); err != nil {
				panic(err)
			}

			certBytes, ok := secret.Data["tls.crt"]
			if !ok {
				panic("no TLS cert found in secret")
			}
			keyBytes, ok := secret.Data["tls.key"]
			if !ok {
				panic("no TLS key found in secret")
			}

			cert, err := tls.X509KeyPair(certBytes, keyBytes)
			if err != nil {
				panic(err)
			}
			httpServer.TLSConfig.Certificates = append(httpServer.TLSConfig.Certificates, cert)
		} else {
			caCert, caKey, err := tlstools.GenerateSelfSignedCA()
			if err != nil {
				panic(err)
			}
			cert, err := tlstools.GenerateTLSCert(caCert, caKey, big.NewInt(1), []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::")})
			if err != nil {
				panic(err)
			}
			httpServer.TLSConfig.Certificates = append(httpServer.TLSConfig.Certificates, cert)
		}
	}

	if len(httpServer.TLSConfig.Certificates) > 0 || httpServer.TLSConfig.GetCertificate != nil {
		log.Fatal(httpServer.ListenAndServeTLS("", ""))
	} else {
		log.Fatal(httpServer.ListenAndServe())
	}
}
