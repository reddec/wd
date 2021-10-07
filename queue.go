package wd

import (
	"container/list"
	"context"
	"sync"
)

// Queue for storing string values for async processing.
type Queue interface {
	// Push value to queue to the back.
	Push(ctx context.Context, value string) error
	// Pop value from front and remove it.
	Pop(ctx context.Context) (string, error)
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

func (q *inMemory) Push(_ context.Context, value string) error {
	q.lock.Lock()
	q.content.PushBack(value)
	q.lock.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:

	}
	return nil
}

func (q *inMemory) Pop(ctx context.Context) (string, error) {
	for {
		q.lock.Lock()
		elem := q.content.Front()
		if elem != nil {
			q.content.Remove(elem)
		}
		q.lock.Unlock()

		if elem != nil {
			return elem.Value.(string), nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-q.notify:
		}
	}
}

// Limited in-memory queue with predefined maximum size
func Limited(size int) Queue {
	return &boundQueue{queue: make(chan string, size)}
}

type boundQueue struct {
	queue chan string
}

func (q *boundQueue) Push(ctx context.Context, value string) error {
	select {
	case q.queue <- value:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *boundQueue) Pop(ctx context.Context) (string, error) {
	select {
	case value := <-q.queue:
		return value, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
