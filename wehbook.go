package wd

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/reddec/wd/internal"
	"golang.org/x/sync/semaphore"
)

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

var (
	ErrUnprocessableFile = errors.New("stored request file unprocessable")
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
	ArgType        ArgType               // how to pass request body to script. Default is by stdin
	RunAsFileOwner bool                  // (posix only) run as user and group same as defined on file (first argument) (ie: gid, uid), must be run as root.
	TempDir        bool                  // create new temp work dir for each request inside main WorkDir
	WorkDir        string                // location for scripts work dir. Acts as parent dir in case TempDir enabled. Also, in case TempDir enabled and WorkDir is empty - default system temp dir will be used
	Timeout        time.Duration         // (can be overridden by xattrs) execution timeout. Zero or negative means no time limit
	BufferSize     int                   // buffer response before reply. Zero means no buffering. It's soft limit.
	Async          AsyncMode             // (can be overridden by xattrs) cache request into temp, returns 202 and process request in background
	Retries        uint                  // (can be overridden by xattrs) number of additional retries after first attempt in case of async processing
	Delay          time.Duration         // (can be overridden by xattrs) delay between retries for async processing. If delay is less or equal to 0, DefaultDelay will be used
	Workers        int64                 // maximum amount of parallel sync requests. If it <= 0, 2 * NumCPU used
	Registerer     prometheus.Registerer // prometheus registry. If not defined - new one will be used. Use prometheus.DefaultRegisterer to expose metrics globally
	Queue          Queue                 // queue for async requests tasks. If not defined - Unbound used
}

type Webhooks struct {
	config      Config
	runner      Runner
	queue       Queue
	syncWorkers *semaphore.Weighted
	// metrics
	workersNum   prometheus.Gauge // number of go-routines running Run() (processing async requests)
	requestsNum  *prometheus.CounterVec
	requestsTime *prometheus.CounterVec
	trafficIn    *prometheus.CounterVec // input traffic
	trafficOut   *prometheus.CounterVec // output traffic

	queuedNum          prometheus.Gauge
	processingNum      prometheus.Gauge
	waitingForRetryNum prometheus.Gauge
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
//
// In case request marked as async, request will be serialized to file, name of file will be pushed to queue.
// Workers (go-routines invoked Run) will pickup file name and will start stream request from file transparently for upstream.
//
// Special header X-Attempt will be added to the request. Attempt is number, starting from 1.
//
// To start async processing, the Run should be invoked.
func New(config Config, runner Runner) *Webhooks {
	if config.Workers <= 0 {
		config.Workers = int64(2 * runtime.NumCPU())
	}
	if config.Queue == nil {
		config.Queue = Unbound()
	}
	if config.Delay <= 0 {
		config.Delay = DefaultDelay
	}

	registry := config.Registerer
	if registry == nil {
		registry = prometheus.NewRegistry()
	}

	factory := promauto.With(registry)

	return &Webhooks{
		config:      config,
		runner:      runner,
		syncWorkers: semaphore.NewWeighted(config.Workers),
		queue:       config.Queue,

		workersNum: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Name:      "workers",
			Help:      "current number of workers",
		}),
		requestsNum: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Name:      "requests",
			Help:      "total number of arrived async requests",
		}, []string{"path", "async"}),
		requestsTime: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Name:      "time",
			Help:      "total time spent to process requests",
		}, []string{"path", "status", "async"}),
		queuedNum: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Name:      "queue",
			Help:      "queue size",
		}),
		processingNum: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Name:      "processing",
			Help:      "number of items which are in processing state",
		}),
		waitingForRetryNum: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Name:      "waiting",
			Help:      "number of items waiting for retry",
		}),
		trafficIn: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Subsystem: "traffic",
			Name:      "input",
			Help:      "total incoming traffic in bytes",
		}, []string{"path"}),
		trafficOut: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Subsystem: "traffic",
			Name:      "output",
			Help:      "total outgoing traffic in bytes",
		}, []string{"path"}),
	}
}

func (wh *Webhooks) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()
	started := time.Now()

	// get manifest or return 404
	manifest := wh.runner.Command(req, wh.defaultManifest(req))
	if manifest == nil {
		http.NotFound(writer, req)
		return
	}

	// count input size
	meter := internal.NewMeteredStream(req.Body)
	defer func() {
		wh.trafficIn.WithLabelValues(req.URL.Path).Add(float64(meter.Total()))
	}()

	req.Body = meter

	// buffered response
	response := internal.NewBufferedStream(writer, wh.config.BufferSize)

	writer = response

	// save metrics
	defer func() {
		wh.requestsTime.WithLabelValues(
			req.URL.Path,
			strconv.Itoa(response.StatusCode()),
			strconv.FormatBool(manifest.Async),
		).Add(time.Since(started).Seconds())
		wh.trafficOut.WithLabelValues(req.URL.Path).Add(float64(response.Total()))
	}()

	defer response.Flush()

	wh.requestsNum.WithLabelValues(req.URL.Path, strconv.FormatBool(manifest.Async)).Inc()

	if manifest.Async {
		if err := wh.enqueueWebhook(req, manifest); err != nil {
			log.Println("failed enqueue task:", err)
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
		return
	}

	// limit number of maximum sync webhooks to prevent overload system
	if err := wh.syncWorkers.Acquire(req.Context(), 1); err != nil {
		log.Println("failed acquire sync worker: %w", err)
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	defer wh.syncWorkers.Release(1)

	err := wh.invokeWebhook(response, req, manifest)
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
	if !response.HeadersSent() {
		response.Header().Set("X-Error", err.Error())
		response.WriteHeader(status)
	}
}

func (wh *Webhooks) invokeWebhook(writer http.ResponseWriter, req *http.Request, manifest *Manifest) error {
	ctx := req.Context()
	if wh.config.Timeout > 0 {
		tCtx, cancel := context.WithTimeout(ctx, wh.config.Timeout)
		defer cancel()
		ctx = tCtx
	}

	// create temp dir
	workDir, err := wh.tempDir(manifest.Binary())
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(writer, req)
		return err
	} else if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed to create temp dir:", err)
		return err
	}
	defer wh.cleanupTempDir(workDir)

	cmd := exec.CommandContext(ctx, manifest.Binary(), manifest.Args()...)
	cmd.Dir = workDir
	cmd.Stdout = writer
	cmd.Env = os.Environ()
	// map headers to env
	for k, v := range req.Header {
		cmd.Env = append(cmd.Env, "HEADER_"+toEnv(k)+"="+strings.Join(v, ","))
	}
	// map query to env
	for k, v := range req.URL.Query() {
		cmd.Env = append(cmd.Env, "QUERY_"+toEnv(k)+"="+strings.Join(v, ","))
	}
	// add special env vars
	cmd.Env = append(cmd.Env,
		"REQUEST_PATH="+req.URL.Path,
		"REQUEST_METHOD="+req.Method,
		"CLIENT_ADDR="+req.RemoteAddr)
	// if applicable - run as owner of the script
	if err := wh.setRunCredentials(cmd, manifest.Binary()); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed set credentials based on file:", err)
		return err
	}
	// read body to var if arg type is env or arg, otherwise pipe to STDIN
	var requestBody string
	if wh.config.ArgType.IsCachingType() {
		data, err := ioutil.ReadAll(req.Body)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			log.Println("failed read request body:", err)
			return err
		}
		requestBody = string(data)
	}

	switch wh.config.ArgType {
	case ArgTypeParam:
		cmd.Args = append(cmd.Args, requestBody)
	case ArgTypeEnv:
		cmd.Env = append(cmd.Env, ArgEnv+"="+requestBody)
	case ArgTypeStdin:
		fallthrough
	default:
		cmd.Stdin = req.Body
	}

	return cmd.Run()
}

func (wh *Webhooks) tempDir(script string) (string, error) {
	if !wh.config.TempDir {
		return wh.config.WorkDir, nil
	}
	tmpDir, err := ioutil.TempDir(wh.config.WorkDir, "")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	if !wh.config.RunAsFileOwner {
		return tmpDir, nil
	}
	if err := internal.ChownAsFile(tmpDir, script); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", fmt.Errorf("chown temp dir %s based on uid/gid from %s: %w", tmpDir, script, err)
	}
	return tmpDir, nil
}

func (wh *Webhooks) cleanupTempDir(dir string) error {
	if !wh.config.TempDir {
		return nil
	}
	return os.RemoveAll(dir)
}

func (wh *Webhooks) setRunCredentials(cmd *exec.Cmd, script string) error {
	if !wh.config.RunAsFileOwner {
		return nil
	}
	return internal.SetCreds(cmd, script)
}

func (wh *Webhooks) defaultManifest(req *http.Request) Manifest {
	return Manifest{
		Async:   wh.isAsyncRequest(req),
		Timeout: wh.config.Timeout,
		Retries: wh.config.Retries,
		Delay:   wh.config.Delay,
	}
}

func toEnv(name string) string {
	return strings.ReplaceAll(strings.ToUpper(name), "-", "_")
}

func (at ArgType) IsCachingType() bool {
	return at == ArgTypeEnv || at == ArgTypeParam
}
