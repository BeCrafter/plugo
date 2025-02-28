package plugo

import (
	"sync/atomic"
	"time"
)

type Metrics struct {
	totalCalls   uint64
	failedCalls  uint64
	responseTime time.Duration
	lastError    error
	startTime    time.Time
}

func (m *Metrics) recordCall(duration time.Duration, err error) {
	atomic.AddUint64(&m.totalCalls, 1)
	if err != nil {
		atomic.AddUint64(&m.failedCalls, 1)
		m.lastError = err
	}
	atomic.AddInt64((*int64)(&m.responseTime), int64(duration))
}

func (m *Metrics) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"total_calls":       atomic.LoadUint64(&m.totalCalls),
		"failed_calls":      atomic.LoadUint64(&m.failedCalls),
		"avg_response_time": time.Duration(atomic.LoadInt64((*int64)(&m.responseTime))) / time.Duration(atomic.LoadUint64(&m.totalCalls)),
		"uptime":            time.Since(m.startTime),
		"last_error":        m.lastError,
	}
}
