package internal

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

func NewMeteredStream(source io.ReadCloser) *MeteredStream {
	return &MeteredStream{
		source: source,
	}
}

// MeteredStream wraps any ReadCloser and count consumed bytes.
type MeteredStream struct {
	read   int
	source io.ReadCloser
}

func (ms *MeteredStream) Read(p []byte) (n int, err error) {
	n, err = ms.source.Read(p)
	ms.read += n
	return
}

func (ms *MeteredStream) Close() error {
	return ms.source.Close()
}

func (ms *MeteredStream) Total() int {
	return ms.read
}

func NewBufferedStream(upstream http.ResponseWriter, bufferSize int) *BufferedResponse {
	return &BufferedResponse{
		bufferSize: bufferSize,
		created:    time.Now(),
		upstream:   upstream,
	}
}

type BufferedResponse struct {
	bufferSize  int
	statusCode  int
	created     time.Time
	headersSent bool
	buffer      bytes.Buffer
	upstream    http.ResponseWriter
	sent        int
}

func (br *BufferedResponse) Header() http.Header {
	return br.upstream.Header()
}

func (br *BufferedResponse) Write(data []byte) (int, error) {
	if br.headersSent || br.bufferSize <= 0 {
		_ = br.Flush()
		v, err := br.upstream.Write(data)
		br.sent += v
		return v, err
	}
	br.buffer.Write(data)
	if br.buffer.Len() < br.bufferSize {
		return len(data), nil
	}
	return len(data), br.Flush()
}

func (br *BufferedResponse) WriteHeader(statusCode int) {
	br.statusCode = statusCode
}

func (br *BufferedResponse) Flush() error {
	if br.headersSent {
		return nil
	}
	if br.statusCode != 0 {
		br.upstream.WriteHeader(br.statusCode)
	} else {
		br.statusCode = http.StatusOK
	}
	br.headersSent = true
	if br.buffer.Len() == 0 {
		return nil
	}
	v, err := br.upstream.Write(br.buffer.Bytes())
	br.sent += v
	br.buffer = bytes.Buffer{} // release allocated memory
	return err
}

func (br *BufferedResponse) StatusCode() int {
	return br.statusCode
}

func (br *BufferedResponse) Total() int {
	return br.sent
}

func (br *BufferedResponse) Duration() time.Duration {
	return time.Since(br.created)
}

func (br *BufferedResponse) HeadersSent() bool {
	return br.headersSent
}
