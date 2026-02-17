package ankylogo

import (
	"container/list"
	"sync"
	"time"
)

// The following block implements the sliding window algorithm

type RateLimiter interface {
	Allow() bool
}

var _ RateLimiter = (*SlidingWindowLimiter)(nil)

type SlidingWindowLimiter struct {
	window int64
	limit  int
	logs   *list.List // deque // push_back, push_front -> in O(1)
	mutex  sync.Mutex
}

func NewSlidingWindowLimiter(window int64, limit int) *SlidingWindowLimiter {
	return &SlidingWindowLimiter{
		window: window,
		limit:  limit,
		logs:   list.New(),
	}
}

func (sw *SlidingWindowLimiter) Allow() bool {
	sw.mutex.Lock()
	defer sw.mutex.Unlock()

	now := time.Now()
	// Use full nanosecond precision instead of truncating to seconds
	edgeTime := now.Add(-time.Duration(sw.window) * time.Second)

	// Remove outdated logs
	for sw.logs.Len() > 0 {
		front := sw.logs.Front()
		if front.Value.(time.Time).Before(edgeTime) {
			sw.logs.Remove(front)
		} else {
			break
		}
	}

	// Check if we can accept the request
	if sw.logs.Len() < sw.limit {
		sw.logs.PushBack(now)
		return true
	}

	return false
}
