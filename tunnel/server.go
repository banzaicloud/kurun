package tunnel

import "net/http"

type Server interface {
	http.RoundTripper
	http.Handler
	Shutdown()
}
