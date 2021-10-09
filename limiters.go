package wd

import (
	"errors"
	"io"
	"net/http"
)

// ErrTooBigRequest used to indicate that stream is bigger than allowed.
var ErrTooBigRequest = errors.New("request payload is too big")

// RequestSizeLimit checks Content-Length (if applicable) to prevent consume request body bigger than allowed;
// the 413 Request Entity Too Large will be automatically returned without passing the request to the handler.
//
// In case Content-Length can not be detected (ie: chunked), request body wrapped to limited reader which will return
// ErrTooBigRequest at the moment when stream will attempt to consume more than allowed.
func RequestSizeLimit(maxSize int64, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.ContentLength > maxSize {
			writer.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if request.ContentLength < 0 {
			request.Body = &sizeLimiter{
				maxSize: maxSize,
				reader:  request.Body,
			}
		}
		handler.ServeHTTP(writer, request)
	})
}

type sizeLimiter struct {
	maxSize  int64
	consumed int64
	err      error
	reader   io.ReadCloser
}

func (sl *sizeLimiter) Read(p []byte) (n int, err error) {
	if sl.err != nil {
		return 0, err
	}
	chunk := int64(len(p))
	left := sl.maxSize - sl.consumed
	if left <= 0 {
		sl.err = ErrTooBigRequest
		return 0, sl.err
	}
	if left < chunk {
		chunk = left
	}
	n, err = sl.reader.Read(p[:chunk])
	sl.err = err
	sl.consumed += int64(n)
	return
}

func (sl *sizeLimiter) Close() error {
	return sl.reader.Close()
}
