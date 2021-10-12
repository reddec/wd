package wd

import (
	"container/list"
	"context"
	"sync"
)

type QueuedWebhook struct {
	RequestFile string
	Manifest    *Manifest
}

// Queue for storing values for async processing.
type Queue interface {
	// Push value to queue to the back.
	Push(ctx context.Context, manifest *QueuedWebhook) error
	// Pop value from front and remove it.
	Pop(ctx context.Context) (*QueuedWebhook, error)
}

// Unbound in-memory queue.
func Unbound() Queue {
	return &inMemory{
		notify:  make(chan struct{}, 1),
		content: list.New(),
	}
}

type inMemory struct {
	content *list.List
	lock    sync.Mutex
	notify  chan struct{}
}

func (q *inMemory) Push(_ context.Context, value *QueuedWebhook) error {
	q.lock.Lock()
	q.content.PushBack(value)
	q.lock.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:

	}
	return nil
}

func (q *inMemory) Pop(ctx context.Context) (*QueuedWebhook, error) {
	for {
		q.lock.Lock()
		elem := q.content.Front()
		if elem != nil {
			q.content.Remove(elem)
		}
		q.lock.Unlock()

		if elem != nil {
			return elem.Value.(*QueuedWebhook), nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-q.notify:
		}
	}
}

// Limited in-memory queue with predefined maximum size
func Limited(size int) Queue {
	return &boundQueue{queue: make(chan *QueuedWebhook, size)}
}

type boundQueue struct {
	queue chan *QueuedWebhook
}

func (q *boundQueue) Push(ctx context.Context, value *QueuedWebhook) error {
	select {
	case q.queue <- value:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *boundQueue) Pop(ctx context.Context) (*QueuedWebhook, error) {
	select {
	case value := <-q.queue:
		return value, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
