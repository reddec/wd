package wd

import (
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
	"strings"
	"time"

	"github.com/reddec/wd/internal"
)

// ArgType defines how to pass request body to the executable.
type ArgType byte

const (
	// ArgTypeStdin can be used to stream request body as stdin. It's default methods and most optimal
	// for memory-constrained installation because data streamed as-is without caching.
	ArgTypeStdin ArgType = iota
	// ArgTypeParam used to pass cached request body as string as last parameter of command. It's convenient but not
	// recommended way.
	ArgTypeParam
	// ArgTypeEnv used to pass cached request body as string as environment variable ArgEnv. Do not use it for
	// requests with payload more than ~2-3KB.
	ArgTypeEnv
)

const ArgEnv = "REQUEST_BODY" // Environment variable for ArgTypeEnv

// Config for webhook daemon. All fields are completely optional.
type Config struct {
	ArgType        ArgType       // how to pass request body to script. Default is by stdin
	RunAsFileOwner bool          // (posix only) run as user and group same as defined on file (first argument) (ie: gid, uid), must be run as root.
	TempDir        bool          // create new temp work dir for each request inside main WorkDir
	WorkDir        string        // location for scripts work dir. Acts as parent dir in case TempDir enabled. Also, in case TempDir enabled and WorkDir is empty - default system temp dir will be used
	Timeout        time.Duration // execution timeout. Zero or negative means no time limit
	BufferSize     int           // buffer response before reply. Zero means no buffering. It's soft limit.
	Metrics        *Metrics      // optional metrics for prometheus
}

// New webhook daemon based on config. Fills all default variables and initializes internal state.
//
// Webhook handler - matches request path as script path in ScriptsDir.
// Converts headers to HEADER_<capital snake case> environment, converts query params to QUERY_<capital snake case>
// environment variables. For example:
//
//      HEADER_CONTENT_TYPE
//      QUERY_PAGE
//
// Additionally passed: REQUEST_PATH, REQUEST_METHOD, CLIENT_ADDR (remote IP:port of incoming connection; not including X-Forwarded-For)
//
// Special parameter for ArgType env - REQUEST_PAYLOAD.
func New(config Config, runner Runner) http.Handler {
	return &webhookDaemon{
		Config: config,
		runner: runner,
	}
}

type webhookDaemon struct {
	Config
	runner Runner
}

func (wh *webhookDaemon) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
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

	command := wh.runner.Command(req)
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
	cmd.Env = os.Environ()
	for k, v := range req.Header {
		cmd.Env = append(cmd.Env, "HEADER_"+toEnv(k)+"="+strings.Join(v, ","))
	}

	for k, v := range req.URL.Query() {
		cmd.Env = append(cmd.Env, "QUERY_"+toEnv(k)+"="+strings.Join(v, ","))
	}
	cmd.Env = append(cmd.Env,
		"REQUEST_PATH="+req.URL.Path,
		"REQUEST_METHOD="+req.Method,
		"CLIENT_ADDR="+req.RemoteAddr)

	if err := wh.setRunCredentials(cmd, command[0]); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed set credentials based on file:", err)
		return
	}

	var requestBody string
	if wh.ArgType.IsCachingType() {
		data, err := ioutil.ReadAll(req.Body)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			log.Println("failed read request body:", err)
			return
		}
		requestBody = string(data)
	}

	switch wh.ArgType {
	case ArgTypeParam:
		cmd.Args = append(cmd.Args, requestBody)
	case ArgTypeEnv:
		cmd.Env = append(cmd.Env, ArgEnv+"="+requestBody)
	case ArgTypeStdin:
		fallthrough
	default:
		cmd.Stdin = req.Body
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

func (wh *webhookDaemon) tempDir(script string) (string, error) {
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

func (wh *webhookDaemon) cleanupTempDir(dir string) error {
	if !wh.TempDir {
		return nil
	}
	return os.RemoveAll(dir)
}

func (wh *webhookDaemon) setRunCredentials(cmd *exec.Cmd, script string) error {
	if !wh.RunAsFileOwner {
		return nil
	}
	return internal.SetCreds(cmd, script)
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

func (at ArgType) IsCachingType() bool {
	return at == ArgTypeEnv || at == ArgTypeParam
}
