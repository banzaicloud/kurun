package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"emperror.dev/errors"
	"github.com/spf13/pflag"

	"github.com/banzaicloud/kurun/tunnel"
	tunnelws "github.com/banzaicloud/kurun/tunnel/websocket"
)

type Params struct {
	controlServerAddress  string
	controlServerCertFile string
	controlServerKeyFile  string
	requestServerAddress  string
	requestServerCertFile string
	requestServerKeyFile  string
}

func run() error {
	params := Params{}

	pflag.StringVar(&params.controlServerAddress, "ctrl-srv-addr", ":10080", "control server address")
	pflag.StringVar(&params.controlServerCertFile, "ctrl-srv-cert", "", "path of the control server TLS certificate file")
	pflag.StringVar(&params.controlServerKeyFile, "ctrl-srv-key", "", "path of the control server TLS private key file")
	pflag.StringVar(&params.requestServerAddress, "req-srv-addr", ":80", "control server address")
	pflag.StringVar(&params.requestServerCertFile, "req-srv-cert", "", "path of the request server TLS certificate file")
	pflag.StringVar(&params.requestServerKeyFile, "req-srv-key", "", "path of the request server TLS private key file")
	pflag.Parse()

	// check params

	controlServerCertSet := params.controlServerCertFile != ""
	controlServerKeySet := params.controlServerKeyFile != ""
	controlServerTLS := controlServerCertSet && controlServerKeySet

	if controlServerCertSet != controlServerKeySet {
		specified, notSpecified := "ctrl-srv-cert", "ctrl-srv-key"
		if controlServerKeySet {
			specified, notSpecified = notSpecified, specified
		}
		return errors.Errorf("if %s is specified %s must be specified too", specified, notSpecified)
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

	tunnelServer := tunnelws.NewServer()

	controlServer := &http.Server{
		Addr:    params.controlServerAddress,
		Handler: tunnelServer,
	}

	controlServerErr := make(chan error, 1)
	go func() {
		defer close(controlServerErr)

		var err error
		if controlServerTLS {
			err = controlServer.ListenAndServeTLS(params.controlServerCertFile, params.controlServerKeyFile)
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
