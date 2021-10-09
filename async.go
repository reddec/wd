package wd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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
	ErrAttemptFailed     = errors.New("attempt failed - non 2xx code returned")
	ErrUnprocessableFile = errors.New("stored request file unprocessable")
)

// AsyncConfig for async processing requests. All fields are completely optional.
type AsyncConfig struct {
	Async      AsyncMode             // cache request into temp, returns 204 and process request in background
	Retries    uint                  // number of additional retries after first attempt in case of async processing
	Delay      time.Duration         // delay between retries for async processing. If delay is less or equal to 0, DefaultDelay will be used
	Workers    int64                 // maximum amount of parallel sync requests. If it <= 0, 2 * NumCPU used
	Queue      Queue                 // queue for async requests tasks. If not defined - Unbound used
	Registerer prometheus.Registerer // prometheus registry. If not defined - new one will be used. Use prometheus.DefaultRegisterer to expose metrics globally
}

// Async processing for requests (see AsyncMode).
//
// In case request marked as async, request will be serialized to file, name of file will be pushed to queue.
// Workers (go-routines invoked Run) will pickup file name and will start stream request from file transparently for upstream.
//
// Special header X-Attempt will be added to the request. Attempt is number, starting from 1.
//
// To start async processing, the Run should be invoked.
func Async(config AsyncConfig, handler http.Handler) *AsyncProcessor {
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

	return &AsyncProcessor{
		queue:       config.Queue,
		processor:   handler,
		config:      config,
		syncWorkers: semaphore.NewWeighted(config.Workers),
		// metrics
		workersNum: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Subsystem: "async",
			Name:      "workers",
			Help:      "current number of workers",
		}),
		asyncRequestsNum: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: "webhooks",
			Subsystem: "async",
			Name:      "requests",
			Help:      "total number of arrived async requests",
		}, []string{"path", "dropped"}),
		queuedNum: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Subsystem: "async",
			Name:      "queue",
			Help:      "queue size",
		}),
		processingNum: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Subsystem: "async",
			Name:      "processing",
			Help:      "number of items which are in processing state",
		}),
		waitingForRetryNum: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "webhooks",
			Subsystem: "async",
			Name:      "waiting",
			Help:      "number of items waiting for retry",
		}),
	}
}

type AsyncProcessor struct {
	queue       Queue
	queueSize   int64
	processor   http.Handler
	config      AsyncConfig
	syncWorkers *semaphore.Weighted

	// metrics
	workersNum         prometheus.Gauge
	asyncRequestsNum   *prometheus.CounterVec
	queuedNum          prometheus.Gauge
	processingNum      prometheus.Gauge
	waitingForRetryNum prometheus.Gauge
}

func (ap *AsyncProcessor) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	if !ap.isAsyncRequest(req) {
		if err := ap.syncWorkers.Acquire(req.Context(), 1); err != nil {
			log.Println("failed acquire worker:", err)
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		defer ap.syncWorkers.Release(1)

		ap.processor.ServeHTTP(writer, req)
		return
	}
	var dropped bool
	defer func() {
		ap.asyncRequestsNum.WithLabelValues(req.URL.Path, strconv.FormatBool(dropped)).Inc()
	}()

	tmpFile, err := ioutil.TempFile("", "")
	if err != nil {
		dropped = true
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed to create temp file:", err)
		return
	}

	if err := req.Write(tmpFile); err != nil {
		_ = tmpFile.Close()
		_ = os.RemoveAll(tmpFile.Name())
		dropped = true
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed to save request to temp file:", err)
		return
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.RemoveAll(tmpFile.Name())
		dropped = true
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed to close temp file:", err)
		return
	}

	if err := ap.queue.Push(req.Context(), tmpFile.Name()); err != nil {
		_ = os.RemoveAll(tmpFile.Name())
		dropped = true
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		log.Println("failed to add entry to queue:", err)
		return
	}
	ap.incQueueSize()
	writer.WriteHeader(http.StatusNoContent)
}

// Run single worker to process background tasks in queue. Can be invoked several times to increase performance.
// Blocks till context canceled.
func (ap *AsyncProcessor) Run(ctx context.Context) {
	ap.workersNum.Inc()
	defer ap.workersNum.Dec()
	for {
		fileName, err := ap.queue.Pop(ctx)
		if err != nil {
			return
		}
		ap.decQueueSize()
		tmpFile, err := ap.openStoredRequestFile(fileName)
		if err != nil {
			log.Println("failed to process", fileName, "-", err)
			continue
		}

		ap.processRequestAsync(ctx, tmpFile)
		_ = tmpFile.Close()
		_ = os.RemoveAll(tmpFile.Name())
	}
}

func (ap *AsyncProcessor) processRequestAsync(ctx context.Context, tmpFile *os.File) {
	ap.processingNum.Inc()
	defer ap.processingNum.Dec()

	var i uint
	for i = 0; i <= ap.config.Retries; i++ {
		err := ap.processRequestAsyncAttempt(ctx, tmpFile, i)
		if err == nil {
			log.Println(i+1, "/", ap.config.Retries+1, "successfully processed async request")
			return
		}
		log.Println(i+1, "/", ap.config.Retries+1, "failed to process async request:", err)
		if i < ap.config.Retries {
			ap.waitingForRetryNum.Inc()
			select {
			case <-ctx.Done():
				ap.waitingForRetryNum.Dec()
				return
			case <-time.After(ap.config.Delay):
			}
			ap.waitingForRetryNum.Dec()
		}
	}
	log.Println("async processing failed after all attempts")
}

func (ap *AsyncProcessor) processRequestAsyncAttempt(ctx context.Context, tmpFile *os.File, attempt uint) error {
	if _, err := tmpFile.Seek(0, 0); err != nil {
		return fmt.Errorf("reset temp file: %w", err)
	}

	reader := bufio.NewReader(tmpFile)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return fmt.Errorf("read request from temp file: %w", err)
	}
	req = req.WithContext(ctx)
	req.Header.Set("X-Attempt", strconv.FormatUint(uint64(attempt+1), 10))

	res := &nopWriter{}
	ap.processor.ServeHTTP(res, req)
	if res.status == 0 || res.status/100 == 2 {
		return nil
	}

	return ErrAttemptFailed
}

func (ap *AsyncProcessor) openStoredRequestFile(fileName string) (*os.File, error) {
	var i uint
	for i = 0; i <= ap.config.Retries; i++ {
		tmpFile, err := os.Open(fileName)
		if err == nil {
			return tmpFile, nil
		}
		log.Println("attempt", i+1, "of", ap.config.Retries, "failed open stored request file:", err)
		if i < ap.config.Retries {
			time.Sleep(ap.config.Delay)
		}
		continue
	}
	return nil, ErrUnprocessableFile
}

func (ap *AsyncProcessor) isAsyncRequest(req *http.Request) bool {
	switch ap.config.Async {
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

func (ap *AsyncProcessor) incQueueSize() int64 {
	ap.queuedNum.Inc()
	return atomic.AddInt64(&ap.queueSize, 1)
}

func (ap *AsyncProcessor) decQueueSize() int64 {
	ap.queuedNum.Dec()
	return atomic.AddInt64(&ap.queueSize, -1)
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
