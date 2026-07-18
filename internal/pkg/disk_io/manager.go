// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package disk_io

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/semaphore"
)

const (
	hddWorkers     = 1
	hddQueueOps    = 128
	hddQueuedBytes = 64 << 20

	ssdWorkers     = 8
	ssdQueueOps    = 512
	ssdQueuedBytes = 256 << 20
)

// ErrClosed is returned when an operation is submitted after shutdown begins.
var ErrClosed = errors.New("disk IO scheduler closed")

type DeviceClass uint8

const (
	DeviceHDD DeviceClass = iota
	DeviceSSD
)

func (c DeviceClass) String() string {
	if c == DeviceSSD {
		return "ssd"
	}
	return "hdd"
}

type DeviceID struct {
	Major uint32
	Minor uint32
}

func (d DeviceID) String() string {
	return strconv.FormatUint(uint64(d.Major), 10) + ":" + strconv.FormatUint(uint64(d.Minor), 10)
}

type deviceInfo struct {
	id    DeviceID
	class DeviceClass
}

type discoverPathFunc func(string) (deviceInfo, bool)
type discoverDevicesFunc func() []deviceInfo

// Linux installs these hooks from device_linux.go. Other platforms use the
// default HDD queue without requiring a platform stub.
var (
	discoverPath    discoverPathFunc
	discoverDevices discoverDevicesFunc
)

type profile struct {
	workers     int
	queueOps    int
	queuedBytes int64
}

func profileFor(class DeviceClass) profile {
	if class == DeviceSSD {
		return profile{workers: ssdWorkers, queueOps: ssdQueueOps, queuedBytes: ssdQueuedBytes}
	}
	return profile{workers: hddWorkers, queueOps: hddQueueOps, queuedBytes: hddQueuedBytes}
}

type metrics struct {
	queuedOps    *prometheus.GaugeVec
	queuedBytes  *prometheus.GaugeVec
	inflight     *prometheus.GaugeVec
	wait         *prometheus.HistogramVec
	duration     *prometheus.HistogramVec
	completed    *prometheus.CounterVec
	processed    *prometheus.CounterVec
	deviceQueues *prometheus.GaugeVec
}

func newMetrics() metrics {
	labels := make([]string, 0, 4)
	labels = append(labels, "device", "device_class", "operation")
	return metrics{
		queuedOps: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "neptune_disk_io_queued_operations",
			Help: "Number of disk IO operations waiting for execution.",
		}, labels),
		queuedBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "neptune_disk_io_queued_bytes",
			Help: "Estimated bytes in disk IO operations waiting for execution.",
		}, labels),
		inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "neptune_disk_io_inflight_operations",
			Help: "Number of disk IO operations currently executing.",
		}, labels),
		wait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "neptune_disk_io_wait_seconds",
			Help:    "Time disk IO callers spend waiting for execution.",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 18),
		}, labels),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "neptune_disk_io_duration_seconds",
			Help:    "Disk IO operation execution duration.",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 18),
		}, labels),
		completed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "neptune_disk_io_completed_operations_total",
			Help: "Number of completed disk IO operations.",
		}, append(labels, "result")),
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "neptune_disk_io_processed_bytes_total",
			Help: "Bytes processed by completed disk IO operations.",
		}, labels),
		deviceQueues: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "neptune_disk_io_device_queues",
			Help: "Discovered disk IO queues by device and class.",
		}, []string{"device", "device_class"}),
	}
}

func (m metrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.queuedOps,
		m.queuedBytes,
		m.inflight,
		m.wait,
		m.duration,
		m.completed,
		m.processed,
		m.deviceQueues,
	}
}

type Manager struct {
	metrics      metrics
	executor     Executor
	devices      map[DeviceID]DeviceClass
	queues       map[DeviceID]*Queue
	defaultQueue *Queue
	closed       bool
	mu           sync.Mutex
}

func New(executor Executor) *Manager {
	m := &Manager{
		metrics:  newMetrics(),
		executor: executor,
		devices:  make(map[DeviceID]DeviceClass),
		queues:   make(map[DeviceID]*Queue),
	}
	if discoverDevices != nil {
		for _, device := range discoverDevices() {
			m.devices[device.id] = device.class
		}
	}
	m.defaultQueue = newQueue(DeviceID{}, DeviceHDD, m.executor, m.metrics)
	return m
}

func (m *Manager) Collectors() []prometheus.Collector {
	return m.metrics.collectors()
}

func (m *Manager) QueueForPath(path string) *Queue {
	if discoverPath == nil {
		return m.defaultQueue
	}

	device, ok := discoverPath(path)
	if !ok {
		return m.defaultQueue
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return m.defaultQueue
	}
	if class, exists := m.devices[device.id]; exists {
		device.class = class
	} else {
		m.devices[device.id] = device.class
	}
	if q, exists := m.queues[device.id]; exists {
		return q
	}
	q := newQueue(device.id, device.class, m.executor, m.metrics)
	m.queues[device.id] = q
	return q
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	queues := make([]*Queue, 0, len(m.queues)+1)
	queues = append(queues, m.defaultQueue)
	for _, q := range m.queues {
		queues = append(queues, q)
	}
	m.mu.Unlock()

	for _, q := range queues {
		q.Close()
	}
}

type job struct {
	ctx      context.Context
	op       Operation
	result   chan Result
	enqueued time.Time
}

type Queue struct {
	metrics    metrics
	executor   Executor
	resultPool sync.Pool
	ctx        context.Context
	jobs       chan job
	cancel     context.CancelFunc
	bytesSem   *semaphore.Weighted
	device     string
	devClass   string
	workers    sync.WaitGroup
	byteLimit  int64
	mu         sync.RWMutex
	closeOnce  sync.Once
	closed     bool
}

func newQueue(id DeviceID, class DeviceClass, executor Executor, m metrics) *Queue {
	return newQueueWithProfile(id, class, profileFor(class), executor, m)
}

func newQueueWithProfile(id DeviceID, class DeviceClass, p profile, executor Executor, m metrics) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	device := id.String()
	if id == (DeviceID{}) {
		device = "default"
	}
	q := &Queue{
		metrics:   m,
		executor:  executor,
		bytesSem:  semaphore.NewWeighted(p.queuedBytes),
		jobs:      make(chan job, p.queueOps),
		ctx:       ctx,
		cancel:    cancel,
		device:    device,
		devClass:  class.String(),
		byteLimit: p.queuedBytes,
	}
	q.resultPool.New = func() any { return make(chan Result, 1) }
	q.metrics.deviceQueues.WithLabelValues(device, q.devClass).Set(1)
	for range p.workers {
		q.workers.Go(q.run)
	}
	return q
}

func (q *Queue) Do(ctx context.Context, op Operation) Result {
	result := q.resultPool.Get().(chan Result)
	if err := q.enqueue(ctx, job{ctx: ctx, op: op, result: result}); err != nil {
		q.resultPool.Put(result)
		return Result{Err: err}
	}
	r := <-result
	q.resultPool.Put(result)
	return r
}

func (q *Queue) enqueue(ctx context.Context, j job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.enqueued = time.Now()
	info := j.op.operationInfo()
	weight := min(max(info.bytes, 1), q.byteLimit)

	waitCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(q.ctx, cancel)
	err := q.bytesSem.Acquire(waitCtx, weight)
	stop()
	cancel()
	if err != nil {
		if q.ctx.Err() != nil {
			return ErrClosed
		}
		return err
	}

	q.mu.RLock()
	if q.closed {
		q.mu.RUnlock()
		q.bytesSem.Release(weight)
		return ErrClosed
	}
	labels := []string{q.device, q.devClass, info.name}
	q.metrics.queuedOps.WithLabelValues(labels...).Inc()
	q.metrics.queuedBytes.WithLabelValues(labels...).Add(float64(info.bytes))
	select {
	case q.jobs <- j:
		q.mu.RUnlock()
		return nil
	case <-ctx.Done():
		q.metrics.queuedOps.WithLabelValues(labels...).Dec()
		q.metrics.queuedBytes.WithLabelValues(labels...).Sub(float64(info.bytes))
		q.mu.RUnlock()
		q.bytesSem.Release(weight)
		return ctx.Err()
	case <-q.ctx.Done():
		q.metrics.queuedOps.WithLabelValues(labels...).Dec()
		q.metrics.queuedBytes.WithLabelValues(labels...).Sub(float64(info.bytes))
		q.mu.RUnlock()
		q.bytesSem.Release(weight)
		return ErrClosed
	}
}

func (q *Queue) run() {
	for j := range q.jobs {
		info := j.op.operationInfo()
		weight := min(max(info.bytes, 1), q.byteLimit)
		labels := make([]string, 0, 4)
		labels = append(labels, q.device, q.devClass, info.name)
		q.metrics.queuedOps.WithLabelValues(labels...).Dec()
		q.metrics.queuedBytes.WithLabelValues(labels...).Sub(float64(info.bytes))
		q.metrics.wait.WithLabelValues(labels...).Observe(time.Since(j.enqueued).Seconds())
		q.metrics.inflight.WithLabelValues(labels...).Inc()

		started := time.Now()
		result := Result{Err: j.ctx.Err()}
		executed := result.Err == nil
		if executed {
			result = q.executor.Execute(j.ctx, j.op)
		}
		q.metrics.duration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
		q.metrics.inflight.WithLabelValues(labels...).Dec()
		resultLabel := "success"
		if result.Err != nil {
			resultLabel = "error"
		}
		q.metrics.completed.WithLabelValues(append(labels, resultLabel)...).Inc()
		if executed && result.N > 0 {
			q.metrics.processed.WithLabelValues(labels...).Add(float64(result.N))
		}
		q.bytesSem.Release(weight)
		j.result <- result
	}
}

func (q *Queue) Close() {
	q.closeOnce.Do(func() {
		q.cancel()
		q.mu.Lock()
		q.closed = true
		close(q.jobs)
		q.mu.Unlock()
		q.workers.Wait()
		q.metrics.deviceQueues.WithLabelValues(q.device, q.devClass).Set(0)
	})
}
