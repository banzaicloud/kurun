package connector

import "net/http"

type ConnectorServer interface {
	http.RoundTripper
}

type ConnectorClient interface {
}

type msg struct {
	uid int
	req []byte
}
