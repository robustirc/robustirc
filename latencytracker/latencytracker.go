package latencytracker

import (
	"bytes"
	"expvar"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// LatencyTracker is an exported (in the expvar package sense) data structure
// which keeps an exponential moving average of the latency for a number of
// hosts.
//
// We use an exponential moving average instead of storing values in a
// ringbuffer and calculating the average because the former is simpler, in
// terms of memory usage and implementation.
//
// See also:
// http://en.wikipedia.org/wiki/Exponential_smoothing#The_exponential_moving_average
type LatencyTracker struct {
	average map[string]time.Duration
	mu      sync.RWMutex
}

func NewLatencyTracker(name string) *LatencyTracker {
	l := new(LatencyTracker)
	l.average = make(map[string]time.Duration)
	expvar.Publish(name, l)
	return l
}

func (l *LatencyTracker) AddSample(host string, latency time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.average[host] = time.Duration(0.2*float64(latency) + 0.8*float64(l.average[host]))
}

func (l *LatencyTracker) Latencies() map[string]time.Duration {
	latencies := make(map[string]time.Duration)
	l.mu.RLock()
	defer l.mu.RUnlock()
	for host, latency := range l.average {
		latencies[host] = latency
	}
	return latencies
}

func (l *LatencyTracker) LatencyFor(host string) time.Duration {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.average[host]
}

func (l *LatencyTracker) String() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var b bytes.Buffer
	fmt.Fprintf(&b, "{")
	first := true
	for host, latency := range l.average {
		if !first {
			fmt.Fprintf(&b, ", ")
		}
		fmt.Fprintf(&b, "%q: %v", host, strconv.FormatInt(latency.Nanoseconds(), 10))
		first = false
	}
	fmt.Fprintf(&b, "}")
	return b.String()
}
