// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//
// Written by Maxim Khitrov (November 2012)
//

// Package flowrate provides the tools for monitoring and limiting the flow rate
// of an arbitrary data stream.
package flowrate

import (
	"io"
	"math"
	"sync"
	"time"
)

// Monitor monitors and limits the transfer rate of a data stream.
type Monitor struct {
	mu      sync.Mutex    // Mutex guarding access to all internal fields
	active  bool          // Flag indicating an active transfer
	start   time.Duration // Transfer start time (clock() value)
	total   int64         // Total number of total transferred
	samples int64         // Total number of samples taken

	rSample float64 // Most recent transfer rate sample (total per second)
	rEMA    float64 // Exponential moving average of rSample
	rPeak   float64 // Peak transfer rate (max of all rSamples)
	rWindow float64 // rEMA window (seconds)

	sBytes int64         // Number of total transferred since sLast
	sLast  time.Duration // Most recent sample time (stop time when inactive)
	sRate  time.Duration // Sampling rate

	tLast time.Duration // Time of the most recent transfer of at least 1 byte
}

// New creates a new flow control monitor. Instantaneous transfer rate is
// measured and updated for each sampleRate interval. windowSize determines the
// weight of each sample in the exponential moving average (EMA) calculation.
// The exact formulas are:
//
//	sampleTime = currentTime - prevSampleTime
//	sampleRate = byteCount / sampleTime
//	weight     = 1 - exp(-sampleTime/windowSize)
//	newRate    = weight*sampleRate + (1-weight)*oldRate
//
// The default values for sampleRate and windowSize (if <= 0) are 100ms and 1s,
// respectively.
func New(sampleRate, windowSize time.Duration) *Monitor {
	if sampleRate = clockRound(sampleRate); sampleRate <= 0 {
		sampleRate = 5 * clockRate
	}
	if windowSize <= 0 {
		windowSize = 1 * time.Second
	}
	now := clock()
	return &Monitor{
		active:  true,
		start:   now,
		rWindow: windowSize.Seconds(),
		sLast:   now,
		sRate:   sampleRate,
		tLast:   now,
	}
}

// Update records the transfer of n total and returns n. It should be called
// after each Read/Write operation, even if n is 0.
func (m *Monitor) Update(n int) int {
	m.mu.Lock()
	m.update(n)
	m.mu.Unlock()
	return n
}

// IO is a convenience method intended to wrap io.Reader and io.Writer method
// execution. It calls m.Update(n) and then returns (n, err) unmodified.
func (m *Monitor) IO(n int, err error) (int, error) {
	return m.Update(n), err
}

type wrappedReader struct {
	r io.Reader
	m *Monitor
}

func (w wrappedReader) Read(p []byte) (n int, err error) {
	return w.m.IO(w.r.Read(p))
}

func (m *Monitor) WrapReader(r io.Reader) io.Reader {
	return &wrappedReader{
		r: r,
		m: m,
	}
}

// IO64 is just like IO, but accept int64.
func (m *Monitor) IO64(n int64, err error) (int, error) {
	return m.Update(int(n)), err
}

// Done marks the transfer as finished and prevents any further updates or
// limiting. Instantaneous and current transfer rates drop to 0. Update, IO, and
// Limit methods become NOOPs. It returns the total number of total transferred.
func (m *Monitor) Done() int64 {
	m.mu.Lock()
	if now := m.update(0); m.sBytes > 0 {
		m.reset(now)
	}
	m.active = false
	m.tLast = 0
	n := m.total
	m.mu.Unlock()
	return n
}

// timeRemLimit is the maximum Status.TimeRem value.
const timeRemLimit = 999*time.Hour + 59*time.Minute + 59*time.Second

// Status represents the current Monitor status. All transfer rates are in total
// per second rounded to the nearest byte.
type Status struct {
	Start    time.Time     // Transfer start time
	Duration time.Duration // Time period covered by the statistics
	Idle     time.Duration // Time since the last transfer of at least 1 byte
	Total    int64         // Total number of total transferred
	Samples  int64         // Total number of samples taken
	InstRate int64         // Instantaneous transfer rate
	CurRate  int64         // Current transfer rate (EMA of InstRate)
	AvgRate  int64         // Average transfer rate (Total / Duration)
	PeakRate int64         // Maximum instantaneous transfer rate
	BytesRem int64         // Number of total remaining in the transfer
	TimeRem  time.Duration // Estimated time to completion
	Active   bool          // Flag indicating an active transfer
}

// Status returns current transfer status information. The returned value
// becomes static after a call to Done.
func (m *Monitor) Status() Status {
	m.mu.Lock()
	now := m.update(0)
	s := Status{
		Active:   m.active,
		Start:    clockToTime(m.start),
		Duration: m.sLast - m.start,
		Idle:     now - m.tLast,
		Total:    m.total,
		Samples:  m.samples,
		PeakRate: round(m.rPeak),
	}
	if s.BytesRem < 0 {
		s.BytesRem = 0
	}
	if s.Duration > 0 {
		rAvg := float64(s.Total) / s.Duration.Seconds()
		s.AvgRate = round(rAvg)
		if s.Active {
			s.InstRate = round(m.rSample)
			s.CurRate = round(m.rEMA)
			if s.BytesRem > 0 {
				if tRate := 0.8*m.rEMA + 0.2*rAvg; tRate > 0 {
					ns := float64(s.BytesRem) / tRate * 1e9
					if ns > float64(timeRemLimit) {
						ns = float64(timeRemLimit)
					}
					s.TimeRem = clockRound(time.Duration(ns))
				}
			}
		}
	}
	m.mu.Unlock()
	return s
}

// update accumulates the transferred byte count for the current sample until
// clock() - m.sLast >= m.sRate. The monitor status is updated once the current
// sample is done.
func (m *Monitor) update(n int) (now time.Duration) {
	if !m.active {
		return
	}
	if now = clock(); n > 0 {
		m.tLast = now
	}
	m.sBytes += int64(n)
	if sTime := now - m.sLast; sTime >= m.sRate {
		t := sTime.Seconds()
		if m.rSample = float64(m.sBytes) / t; m.rSample > m.rPeak {
			m.rPeak = m.rSample
		}

		// Exponential moving average using a method similar to *nix load
		// average calculation. Longer sampling periods carry greater weight.
		if m.samples > 0 {
			w := math.Exp(-t / m.rWindow)
			m.rEMA = m.rSample + w*(m.rEMA-m.rSample)
		} else {
			m.rEMA = m.rSample
		}
		m.reset(now)
	}
	return
}

func (m *Monitor) Reset() {
	now := clock()
	m.mu.Lock()
	m.rSample = 0
	m.rEMA = 0
	m.rPeak = 0
	m.start = now
	m.total = 0
	m.sBytes = 0
	m.tLast = now
	m.sLast = now
	m.samples = 0
	m.mu.Unlock()
}

// reset clears the current sample state in preparation for the next sample.
func (m *Monitor) reset(sampleTime time.Duration) {
	m.total += m.sBytes
	m.samples++
	m.sBytes = 0
	m.sLast = sampleTime
}
