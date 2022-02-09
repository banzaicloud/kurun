package websocket

import (
	"net"
	"net/http"
	"unsafe"

	"emperror.dev/errors"
)

func isTemporaryError(err error) bool {
	if e := new(net.Error); errors.As(err, e) {
		return (*e).Temporary()
	}
	return false
}

type requestID = uint64

func getRequestID(r *http.Request) requestID {
	return uint64(uintptr(unsafe.Pointer(r)))
}

func reasonToError(reason interface{}, context string) (err error) {
	if reason == nil {
		err = errors.Errorf("goexit %s", context)
	} else {
		msg := "panic %s"
		if e, ok := reason.(error); ok {
			err = errors.WithMessagef(e, msg, context)
		} else {
			err = errors.WithDetails(errors.Errorf(msg, context), "reason", reason)
		}
	}
	return
}
