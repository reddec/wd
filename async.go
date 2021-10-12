package wd

import (
	"bufio"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (wh *Webhooks) enqueueWebhook(req *http.Request, manifest *Manifest) error {
	// dump request
	tmpFile, err := ioutil.TempFile("", "")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if err := req.Write(tmpFile); err != nil {
		_ = tmpFile.Close()
		_ = os.RemoveAll(tmpFile.Name())
		return fmt.Errorf("serialize request: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.RemoveAll(tmpFile.Name())
		return fmt.Errorf("close temp file: %w", err)
	}

	// add to queue
	if err := wh.queue.Push(req.Context(), &QueuedWebhook{
		RequestFile: tmpFile.Name(),
		Manifest:    manifest,
	}); err != nil {
		_ = os.RemoveAll(tmpFile.Name())
		return fmt.Errorf("push to queue: %w", err)
	}
	wh.queuedNum.Inc()
	return nil
}

// Run single worker to process background tasks in queue. Can be invoked several times to increase performance.
// Blocks till context canceled.
func (wh *Webhooks) Run(ctx context.Context) {
	wh.workersNum.Inc()
	defer wh.workersNum.Dec()
	for {
		enqueuedItem, err := wh.queue.Pop(ctx)
		if err != nil {
			return
		}
		wh.queuedNum.Dec()
		tmpFile, err := wh.openStoredRequestFile(enqueuedItem)
		if err != nil {
			log.Println("failed to process", enqueuedItem.RequestFile, "-", err)
			continue
		}

		wh.processRequestAsync(ctx, enqueuedItem.Manifest, tmpFile)
		_ = tmpFile.Close()
		_ = os.RemoveAll(tmpFile.Name())
	}
}

func (wh *Webhooks) processRequestAsync(ctx context.Context, manifest *Manifest, tmpFile *os.File) {
	wh.processingNum.Inc()
	defer wh.processingNum.Dec()

	var i uint
	for i = 0; i <= manifest.Retries; i++ {
		err := wh.processRequestAsyncAttempt(ctx, tmpFile, manifest, i)
		if err == nil {
			log.Println(i+1, "/", manifest.Retries+1, "successfully processed async request")
			return
		}
		log.Println(i+1, "/", manifest.Retries+1, "failed to process async request:", err)
		if i < manifest.Retries {
			wh.waitingForRetryNum.Inc()
			select {
			case <-ctx.Done():
				wh.waitingForRetryNum.Dec()
				return
			case <-time.After(manifest.Delay):
			}
			wh.waitingForRetryNum.Dec()
		}
	}
	log.Println("async processing failed after all attempts")
}

func (wh *Webhooks) processRequestAsyncAttempt(ctx context.Context, tmpFile *os.File, manifest *Manifest, attempt uint) error {
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
	if err := wh.invokeWebhook(res, req, manifest); err != nil {
		return fmt.Errorf("attempt %d: %w", attempt, err)
	}

	return nil
}

func (wh *Webhooks) openStoredRequestFile(item *QueuedWebhook) (*os.File, error) {
	var i uint
	for i = 0; i <= item.Manifest.Retries; i++ {
		tmpFile, err := os.Open(item.RequestFile)
		if err == nil {
			return tmpFile, nil
		}
		log.Println("attempt", i+1, "of", item.Manifest.Retries, "failed open stored request file:", err)
		if i < item.Manifest.Retries {
			time.Sleep(item.Manifest.Delay)
		}
		continue
	}
	return nil, ErrUnprocessableFile
}

func (wh *Webhooks) isAsyncRequest(mode AsyncMode, req *http.Request) bool {
	switch mode {
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
