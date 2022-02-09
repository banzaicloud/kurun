package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"

	"emperror.dev/errors"
	"github.com/go-logr/stdr"
	"github.com/spf13/pflag"

	"github.com/banzaicloud/kurun/tunnel"
	"github.com/banzaicloud/kurun/tunnel/pkg/tlstools"
	tunnelws "github.com/banzaicloud/kurun/tunnel/websocket"
)

type Params struct {
	controlServerAddress    string
	controlServerSelfSigned bool
	controlServerCertFile   string
	controlServerKeyFile    string
	requestServerAddress    string
	requestServerCertFile   string
	requestServerKeyFile    string
	logVerbosity            int
}

func run() error {
	params := Params{}

	pflag.StringVar(&params.controlServerAddress, "ctrl-srv-addr", ":10080", "control server address")
	pflag.BoolVar(&params.controlServerSelfSigned, "ctrl-srv-self-signed", false, "generate self-signed TLS certificate for control server")
	pflag.StringVar(&params.controlServerCertFile, "ctrl-srv-cert", "", "path of the control server TLS certificate file")
	pflag.StringVar(&params.controlServerKeyFile, "ctrl-srv-key", "", "path of the control server TLS private key file")
	pflag.StringVar(&params.requestServerAddress, "req-srv-addr", ":80", "control server address")
	pflag.StringVar(&params.requestServerCertFile, "req-srv-cert", "", "path of the request server TLS certificate file")
	pflag.StringVar(&params.requestServerKeyFile, "req-srv-key", "", "path of the request server TLS private key file")
	pflag.CountVarP(&params.logVerbosity, "verbose", "v", "logging verbosity")
	pflag.Parse()

	// check params

	controlServerCertSet := params.controlServerCertFile != ""
	controlServerKeySet := params.controlServerKeyFile != ""
	controlServerTLSFromFiles := controlServerCertSet && controlServerKeySet
	controlServerTLS := controlServerTLSFromFiles || params.controlServerSelfSigned

	if controlServerCertSet != controlServerKeySet {
		specified, notSpecified := "ctrl-srv-cert", "ctrl-srv-key"
		if controlServerKeySet {
			specified, notSpecified = notSpecified, specified
		}
		return errors.Errorf("if %s is specified %s must be specified too", specified, notSpecified)
	}

	if controlServerTLSFromFiles && params.controlServerSelfSigned {
		return errors.New("either ctrl-srv-self-signed or ctrl-srv-cert and ctrl-srv-key can be specified")
	}

	requestServerCertSet := params.requestServerCertFile != ""
	requestServerKeySet := params.requestServerKeyFile != ""
	requestServerTLS := requestServerCertSet && requestServerKeySet

	if requestServerCertSet != requestServerKeySet {
		specified, notSpecified := "req-srv-cert", "req-srv-key"
		if requestServerKeySet {
			specified, notSpecified = notSpecified, specified
		}
		return errors.Errorf("if %s is specified %s must be specified too", specified, notSpecified)
	}

	// start servers

	stdr.SetVerbosity(params.logVerbosity)
	logger := stdr.New(log.New(os.Stdout, "", log.LstdFlags|log.LUTC))

	tunnelServer := tunnelws.NewServer(tunnelws.WithLogger(logger))

	controlServer := &http.Server{
		Addr:    params.controlServerAddress,
		Handler: tunnelServer,
	}

	if controlServerTLS {
		certs := []tls.Certificate{}
		if controlServerTLSFromFiles {
			cert, err := tls.LoadX509KeyPair(params.controlServerCertFile, params.controlServerKeyFile)
			if err != nil {
				return err
			}
			certs = append(certs, cert)
		}
		if params.controlServerSelfSigned {
			caCert, caKey, err := tlstools.GenerateSelfSignedCA()
			if err != nil {
				return err
			}
			cert, err := tlstools.GenerateTLSCert(caCert, caKey, big.NewInt(1), []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::")})
			if err != nil {
				return err
			}
			certs = append(certs, cert)
		}
		controlServer.TLSConfig = &tls.Config{
			Certificates: certs,
		}
	}

	controlServerErr := make(chan error, 1)
	go func() {
		defer close(controlServerErr)

		var err error
		if controlServerTLS {
			err = controlServer.ListenAndServeTLS("", "")
		} else {
			err = controlServer.ListenAndServe()
		}

		if err = ignoreServerClosed(err); err != nil {
			controlServerErr <- err
		}
	}()

	requestServer := http.Server{
		Addr:    params.requestServerAddress,
		Handler: tunnel.NewRequestHandler(tunnelServer),
	}

	requestServerErr := make(chan error, 1)
	go func() {
		defer close(requestServerErr)

		var err error
		if requestServerTLS {
			err = requestServer.ListenAndServeTLS(params.requestServerCertFile, params.requestServerKeyFile)
		} else {
			err = requestServer.ListenAndServe()
		}

		if err = ignoreServerClosed(err); err != nil {
			requestServerErr <- err
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// wait

	var lastErr error
	select {
	case err := <-controlServerErr:
		lastErr = errors.Append(lastErr, err)
		lastErr = errors.Append(lastErr, ignoreServerClosed(requestServer.Shutdown(context.Background())))
	case err := <-requestServerErr:
		lastErr = errors.Append(lastErr, err)
		lastErr = errors.Append(lastErr, ignoreServerClosed(controlServer.Shutdown(context.Background())))
	case <-interrupt:
		fmt.Fprintln(os.Stdout, "Shutting down...")
		lastErr = errors.Append(lastErr, ignoreServerClosed(requestServer.Shutdown(context.Background())))
		lastErr = errors.Append(lastErr, ignoreServerClosed(controlServer.Shutdown(context.Background())))
	}

	cerr := <-controlServerErr
	lastErr = errors.Append(lastErr, cerr)
	rerr := <-requestServerErr
	lastErr = errors.Append(lastErr, rerr)

	return lastErr
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%+v\n", err)
	}
}

func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
