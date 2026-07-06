package flowrate

import (
	"math/rand/v2"
	"testing"
	"time"
)

// simulateChunk mimics a piece chunk arriving from one peer.
// chunkSize is typically 16KB in BitTorrent.
const chunkSize = 16 * 1024

// TestPeerVsDownloadWindow simulates 3 peers each with a 100ms monitor
// feeding into a download-level 1s monitor. All receive the same total
// data. After steady state, verify that the sum of peer CurRates is not
// systematically below the download CurRate.
func TestPeerVsDownloadWindow(t *testing.T) {
	dl := New(time.Second, time.Second)

	const numPeers = 3
	peers := make([]*Monitor, numPeers)
	for i := range peers {
		peers[i] = New(100*time.Millisecond, 100*time.Millisecond)
	}

	// Warm up: feed data for 5 seconds to reach steady state,
	// then sample rates over the next 5 seconds.
	const warmup = 5 * time.Second
	const measure = 5 * time.Second
	const totalDuration = warmup + measure

	// Total rate across all peers (bytes per second).
	const totalRate = 10 * 1024 * 1024 // 10 MiB/s
	perPeerRate := totalRate / numPeers

	// Use real time: schedule chunk deliveries at appropriate intervals.
	// For 10 MiB/s total over 3 peers, that's ~208 chunks/sec per peer
	// (or ~625 total). With 3 peers and 10s duration: ~6250 chunks.
	// Each peer delivers perPeerRate bytes/sec ÷ chunkSize chunks/sec.
	chunksPerSecPerPeer := perPeerRate / chunkSize
	interval := time.Second / time.Duration(chunksPerSecPerPeer)

	done := make(chan struct{})
	go func() {
		defer close(done)
		start := time.Now()
		for time.Since(start) < totalDuration {
			for i := range peers {
				peers[i].Update(chunkSize)
				dl.Update(chunkSize)
				// Random jitter ±20% to simulate real network.
				jitter := time.Duration(rand.Int64N(int64(interval) * 40 / 100))
				time.Sleep(interval + jitter - interval/5)
			}
		}
	}()
	<-done

	// Now sample: after steady state, compare rates.
	dlStatus := dl.Status()
	t.Logf("download: CurRate=%d (%.2f MiB/s), Total=%d, Samples=%d",
		dlStatus.CurRate, float64(dlStatus.CurRate)/1024/1024, dlStatus.Total, dlStatus.Samples)

	var peerTotalCurRate int64
	var peerTotalBytes int64
	for i, p := range peers {
		s := p.Status()
		peerTotalCurRate += s.CurRate
		peerTotalBytes += s.Total
		t.Logf("peer %d: CurRate=%d (%.2f MiB/s), Total=%d, Samples=%d",
			i, s.CurRate, float64(s.CurRate)/1024/1024, s.Total, s.Samples)
	}

	t.Logf("sum peer CurRate=%d (%.2f MiB/s), dl CurRate=%d (%.2f MiB/s)",
		peerTotalCurRate, float64(peerTotalCurRate)/1024/1024, dlStatus.CurRate, float64(dlStatus.CurRate)/1024/1024)

	// Total bytes should be exactly equal (modulo timing).
	if dlStatus.Total != peerTotalBytes {
		t.Errorf("total bytes mismatch: dl=%d, peers=%d", dlStatus.Total, peerTotalBytes)
	}

	// CurRate sum should be within 20% of download rate.
	ratio := float64(peerTotalCurRate) / float64(dlStatus.CurRate)
	t.Logf("ratio (peer sum / dl): %.3f", ratio)
	if ratio < 0.8 || ratio > 1.2 {
		t.Errorf("peer sum CurRate (%d) deviates too far from dl CurRate (%d), ratio=%.3f",
			peerTotalCurRate, dlStatus.CurRate, ratio)
	}
}

// TestPeerVsDownloadWindowWithStatusCalls adds frequent Status() calls
// (mimicking debug page refreshes) to see if they affect the rate.
func TestPeerVsDownloadWindowWithStatusCalls(t *testing.T) {
	dl := New(time.Second, time.Second)

	const numPeers = 3
	peers := make([]*Monitor, numPeers)
	for i := range peers {
		peers[i] = New(100*time.Millisecond, 100*time.Millisecond)
	}

	const duration = 5 * time.Second
	const totalRate = 10 * 1024 * 1024
	perPeerRate := totalRate / numPeers
	chunksPerSecPerPeer := perPeerRate / chunkSize
	interval := time.Second / time.Duration(chunksPerSecPerPeer)

	done := make(chan struct{})
	go func() {
		defer close(done)
		start := time.Now()
		for time.Since(start) < duration {
			for i := range peers {
				peers[i].Update(chunkSize)
				dl.Update(chunkSize)
				jitter := time.Duration(rand.Int64N(int64(interval) * 40 / 100))
				time.Sleep(interval + jitter - interval/5)
			}
		}
	}()

	// Aggressive Status() polling: every 50ms, like a user refreshing debug page rapidly.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var dlSamples, peerSamples [][]int64
loop:
	for {
		select {
		case <-done:
			break loop
		case <-ticker.C:
			var sum int64
			for _, p := range peers {
				sum += p.Status().CurRate
			}
			peerSamples = append(peerSamples, []int64{sum})
			dlSamples = append(dlSamples, []int64{dl.Status().CurRate})
		}
	}

	dlStatus := dl.Status()
	var peerTotalCurRate int64
	for i, p := range peers {
		s := p.Status()
		peerTotalCurRate += s.CurRate
		t.Logf("peer %d: CurRate=%d (%.2f MiB/s), Total=%d",
			i, s.CurRate, float64(s.CurRate)/1024/1024, s.Total)
	}
	t.Logf("dl: CurRate=%d (%.2f MiB/s), Total=%d",
		dlStatus.CurRate, float64(dlStatus.CurRate)/1024/1024, dlStatus.Total)

	// Average over samples.
	var dlAvg, peerAvg int64
	for i := range dlSamples {
		dlAvg += dlSamples[i][0]
		peerAvg += peerSamples[i][0]
	}
	if len(dlSamples) > 0 {
		dlAvg /= int64(len(dlSamples))
		peerAvg /= int64(len(peerSamples))
	}
	t.Logf("average over %d samples: dl=%d (%.2f MiB/s), peer sum=%d (%.2f MiB/s), ratio=%.3f",
		len(dlSamples), dlAvg, float64(dlAvg)/1024/1024, peerAvg, float64(peerAvg)/1024/1024,
		float64(peerAvg)/float64(dlAvg))

	if dlStatus.Total == 0 {
		t.Error("no data transferred")
	}
}
