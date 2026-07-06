package flowrate

import (
	"math/rand/v2"
	"testing"
	"time"
)

const chunkSize = 16 * 1024

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
