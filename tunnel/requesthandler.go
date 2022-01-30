package tunnel

import (
	"io"
	"net/http"
)

func NewRequestHandler(rt http.RoundTripper) *RequestHandler {
	return &RequestHandler{
		RoundTripper: rt,
	}
}

type RequestHandler struct {
	RoundTripper http.RoundTripper
}

func (rh RequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp, err := rh.RoundTripper.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeResponseToResponseWriter(w, resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func writeResponseToResponseWriter(w http.ResponseWriter, r *http.Response) error {
	if w == nil {
		return nil
	}
	if r == nil {
		return nil
	}
	// copy headers
	// TODO: maybe some headers need to be filtered or transformed
	for key, values := range r.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	// copy status code
	w.WriteHeader(r.StatusCode)
	// copy body
	defer r.Body.Close()
	_, err := io.Copy(w, r.Body)
	return err
}
