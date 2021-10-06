package wd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/reddec/wd/internal"
)

var ErrAttemptFailed = errors.New("attempt failed - non 2xx code returned")

const DefaultDelay = 3 * time.Second

type AsyncMode byte

const (
	// AsyncModeAuto enables async processing mode if there is 'async=(1|y|yes|true|ok|on)' in query
	AsyncModeAuto AsyncMode = iota
	// AsyncModeForced enables async processing mode regardless of anything
	AsyncModeForced
	// AsyncModeDisabled disables async processing
	AsyncModeDisabled
)

// Webhook handler - matches request path as script path in ScriptsDir.
// Converts headers to HEADER_<capital snake case> environment, converts query params to QUERY_<capital snake case>
// environment variables.
//
//      HEADER_CONTENT_TYPE
//      QUERY_PAGE
//
// Special header HEADER_X_ATTEMPT will be added in case of async processing. Attempt is number, starting from 1.
type Webhook struct {
	RunAsFileOwner bool          // (posix only) run as user and group same as defined on file (first argument) (ie: gid, uid), must be run as root.
	TempDir        bool          // create new temp work dir for each request inside main WorkDir
	WorkDir        string        // location for scripts work dir. Acts as parent dir in case TempDir enabled. Also, in case TempDir enabled and WorkDir is empty - default system temp dir will be used
	Timeout        time.Duration // execution timeout. Zero or negative means no time limit
	BufferSize     int           // buffer response before reply. Zero means no buffering. It's soft limit.
	Metrics        *Metrics      // optional metrics for prometheus
	Async          AsyncMode     // cache request into temp, returns 204 and process request in background
	Retries        int           // (async only) number of additional retries after first attempt in case of async processing
	Delay          time.Duration // (async only) delay between retries for async processing. If delay is less or equal to 0, DefaultDelay will be used
	Runner         Runner        // what to run
}

func (wh *Webhook) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	if !wh.isAsyncRequest(req) {
		wh.processRequest(writer, req)
		return
	}
	tmpFile, err := ioutil.TempFile("", "")
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed to create temp file:", err)
		return
	}

	if err := req.Write(tmpFile); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed to save request to temp file:", err)
		return
	}

	writer.WriteHeader(http.StatusNoContent)
	go wh.processRequestAsync(tmpFile)
}

func (wh *Webhook) processRequestAsync(tmpFile *os.File) {
	defer os.RemoveAll(tmpFile.Name())

	delay := wh.Delay
	if delay <= 0 {
		delay = DefaultDelay
	}

	for i := 0; i <= wh.Retries; i++ {
		err := wh.processRequestAsyncAttempt(tmpFile, i)
		if err == nil {
			log.Println(i+1, "/", wh.Retries+1, "successfully processed async request")
			return
		}
		log.Println(i+1, "/", wh.Retries+1, "failed to process async request:", err)
		if i < wh.Retries {
			time.Sleep(delay)
		}
	}
	log.Println("async processing failed after all attempts")
}

func (wh *Webhook) processRequestAsyncAttempt(tmpFile *os.File, attempt int) error {
	if _, err := tmpFile.Seek(0, 0); err != nil {
		return fmt.Errorf("reset temp file: %w", err)
	}

	reader := bufio.NewReader(tmpFile)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return fmt.Errorf("read request from temp file: %w", err)
	}
	req.Header.Set("X-Attempt", strconv.Itoa(attempt+1))

	res := &nopWriter{}
	wh.processRequest(res, req)
	if res.status == 0 || res.status/100 == 2 {
		return nil
	}

	return ErrAttemptFailed
}

func (wh *Webhook) processRequest(writer http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	// count input size
	meter := &meteredStream{source: req.Body}
	req.Body = meter

	// buffered response
	response := &bufferedResponse{
		bufferSize: wh.BufferSize,
		upstream:   writer,
		created:    time.Now(),
	}
	writer = response

	// write to metrics
	defer wh.Metrics.countResult(req, response, meter)
	defer response.flush()

	command := wh.Runner.Command(req)
	if len(command) == 0 {
		http.NotFound(writer, req)
		return
	}

	ctx := req.Context()
	if wh.Timeout > 0 {
		tCtx, cancel := context.WithTimeout(ctx, wh.Timeout)
		defer cancel()
		ctx = tCtx
	}

	// create temp dir
	workDir, err := wh.tempDir(command[0])
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(writer, req)
		return
	} else if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed to create temp dir:", err)
		return
	}
	defer wh.cleanupTempDir(workDir)

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workDir
	cmd.Stdout = writer
	cmd.Stdin = req.Body
	cmd.Env = os.Environ()
	for k, v := range req.Header {
		cmd.Env = append(cmd.Env, "HEADER_"+toEnv(k)+"="+strings.Join(v, ","))
	}

	for k, v := range req.URL.Query() {
		cmd.Env = append(cmd.Env, "QUERY_"+toEnv(k)+"="+strings.Join(v, ","))
	}
	cmd.Env = append(cmd.Env, "REQUEST_PATH="+req.URL.Path, "REQUEST_METHOD="+req.Method)

	if err := wh.setRunCredentials(cmd, command[0]); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed set credentials based on file:", err)
		return
	}

	err = cmd.Run()
	if err == nil {
		return
	}

	var status = http.StatusBadGateway

	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	} else if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	}

	log.Println("failed run webhook:", err)
	if !response.headersSent {
		response.Header().Set("X-Error", err.Error())
		response.WriteHeader(status)
	}

	return

}

func (wh *Webhook) tempDir(script string) (string, error) {
	if !wh.TempDir {
		return wh.WorkDir, nil
	}
	tmpDir, err := ioutil.TempDir(wh.WorkDir, "")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	if !wh.RunAsFileOwner {
		return tmpDir, nil
	}
	if err := internal.ChownAsFile(tmpDir, script); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", fmt.Errorf("chown temp dir %s based on uid/gid from %s: %w", tmpDir, script, err)
	}
	return tmpDir, nil
}

func (wh *Webhook) cleanupTempDir(dir string) error {
	if !wh.TempDir {
		return nil
	}
	return os.RemoveAll(dir)
}

func (wh *Webhook) setRunCredentials(cmd *exec.Cmd, script string) error {
	if !wh.RunAsFileOwner {
		return nil
	}
	return internal.SetCreds(cmd, script)
}

func (wh *Webhook) isAsyncRequest(req *http.Request) bool {
	switch wh.Async {
	case AsyncModeDisabled:
		return false
	case AsyncModeForced:
		return true
	case AsyncModeAuto:
		fallthrough
	default:
		return parseBool(req.URL.Query().Get("async"))
	}
}

func toEnv(name string) string {
	return strings.ReplaceAll(strings.ToUpper(name), "-", "_")
}

type bufferedResponse struct {
	bufferSize  int
	statusCode  int
	created     time.Time
	headersSent bool
	buffer      bytes.Buffer
	upstream    http.ResponseWriter
	sent        int
}

func (br *bufferedResponse) Header() http.Header {
	return br.upstream.Header()
}

func (br *bufferedResponse) Write(data []byte) (int, error) {
	if br.headersSent || br.bufferSize <= 0 {
		_ = br.flush()
		v, err := br.upstream.Write(data)
		br.sent += v
		return v, err
	}
	br.buffer.Write(data)
	if br.buffer.Len() < br.bufferSize {
		return len(data), nil
	}
	return len(data), br.flush()
}

func (br *bufferedResponse) WriteHeader(statusCode int) {
	br.statusCode = statusCode
}

func (br *bufferedResponse) flush() error {
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

type meteredStream struct {
	read   int
	source io.ReadCloser
}

func (ms *meteredStream) Read(p []byte) (n int, err error) {
	n, err = ms.source.Read(p)
	ms.read += n
	return
}

func (ms *meteredStream) Close() error {
	return ms.source.Close()
}

type nopWriter struct {
	status  int
	headers http.Header
}

func (nw *nopWriter) Header() http.Header {
	if nw.headers == nil {
		nw.headers = make(http.Header)
	}
	return nw.headers
}

func (nw *nopWriter) Write(i []byte) (int, error) {
	return len(i), nil
}

func (nw *nopWriter) WriteHeader(statusCode int) {
	nw.status = statusCode
}

func parseBool(value string) bool {
	switch strings.ToLower(value) {
	case "t", "1", "on", "ok", "true", "yes":
		return true
	default:
		return false
	}
}
