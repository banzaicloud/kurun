package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"github.com/banzaicloud/kurun/tunnel"
	tunnelws "github.com/banzaicloud/kurun/tunnel/websocket"
	"github.com/gorilla/websocket"
	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	var (
		downstream  string
		namespace   string
		podName     string
		port        string
		serviceName string
		tlsSecret   string
		verbose     bool
	)
	pflag.StringVarP(&namespace, "namespace", "n", "default", "resource namespace")
	pflag.StringVar(&podName, "pod", "", "reference to the K8s pod to connect to")
	pflag.StringVarP(&port, "port", "p", "", "port to connect to")
	pflag.StringVar(&serviceName, "service", "", "reference to the K8s service to connect to")
	pflag.StringVar(&tlsSecret, "tlssecret", "", "reference to a K8s secret containing TLS CA cert")
	pflag.BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	pflag.Parse()

	if pflag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "downstream URL must be specified")
	}
	downstream = pflag.Arg(0)

	downstreamURL, err := url.Parse(downstream)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not parse downstream URL:", err)
	}
	if downstreamURL.Scheme == "" && tlsSecret != "" {
		downstreamURL.Scheme = "https"
	}

	if port == "" {
		fmt.Fprintln(os.Stderr, "connection port must be specified")
		pflag.Usage()
		return
	}

	if podName == "" && serviceName == "" {
		fmt.Fprintln(os.Stderr, "either a pod or a service has to be specified to connect to")
		pflag.Usage()
		return
	}
	if podName != "" && serviceName != "" {
		fmt.Fprintln(os.Stderr, "either a pod or a service can be specified to connect to")
		pflag.Usage()
		return
	}
	resources, resource := "pods", podName
	if serviceName != "" {
		resources, resource = "services", serviceName
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		select {
		case <-signals:
			cancel()
			fmt.Fprintln(os.Stdout, "client interrupted, terminating")
		case <-ctx.Done():
		}
	}()

	restCfg := config.GetConfigOrDie()
	tlsCfg, err := rest.TLSConfigFor(restCfg)
	if err != nil {
		panic(err)
	}

	baseTransport := &http.Transport{}
	if tlsSecret != "" {
		secretKey := client.ObjectKey{
			Namespace: namespace,
			Name:      tlsSecret,
		}
		if parts := strings.SplitN(tlsSecret, "/", 1); len(parts) > 1 {
			secretKey.Namespace, secretKey.Name = parts[0], parts[1]
		}

		kubeClient, err := client.New(restCfg, client.Options{})
		if err != nil {
			panic(err)
		}

		var secret v1.Secret
		if err := kubeClient.Get(ctx, secretKey, &secret); err != nil {
			panic(err)
		}

		caBytes, ok := secret.Data["ca.crt"]
		if !ok {
			panic("no ca cert found in secret")
		}

		if baseTransport.TLSClientConfig == nil {
			baseTransport.TLSClientConfig = &tls.Config{}
		}
		if baseTransport.TLSClientConfig.RootCAs == nil {
			baseTransport.TLSClientConfig.RootCAs = x509.NewCertPool()
		}
		if !baseTransport.TLSClientConfig.RootCAs.AppendCertsFromPEM(caBytes) {
			panic("unable to append CA")
		}
	}

	transport := tunnel.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r.RequestURI = ""

		r.URL.Scheme = downstreamURL.Scheme
		r.URL.Host = downstreamURL.Host
		if downstreamURL.Path != "" {
			r.URL.Path = path.Join(downstreamURL.Path, r.URL.Path)
		}
		return baseTransport.RoundTrip(r)
	})

	proxyURL, err := url.Parse(restCfg.Host)
	if err != nil {
		panic(err)
	}
	if proxyURL.Scheme != "https" {
		panic("API server not secure")
	}
	proxyURL.Scheme = "wss"

	proxyURL.Path = fmt.Sprintf("/api/v1/namespaces/%s/%s/https:%s:%s/proxy/", namespace, resources, resource, port)
	if verbose {
		fmt.Fprintln(os.Stdout, "proxy url:", proxyURL.String())
	}

	tunnelClientCfg := tunnelws.NewClientConfig(proxyURL.String(), transport, tunnelws.WithDialerCtor(func() *websocket.Dialer {
		return &websocket.Dialer{
			TLSClientConfig: tlsCfg.Clone(),
		}
	}))
	go func() {
		if err := tunnelws.RunClient(ctx, *tunnelClientCfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	<-ctx.Done()
}
