package sysload

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu       sync.Mutex
	counters []Counters
	err      error
	gpuCalls int
	gpus     []GPU
}

func (f *fakeSource) Counters(context.Context) (Counters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Counters{}, f.err
	}
	c := f.counters[0]
	if len(f.counters) > 1 {
		f.counters = f.counters[1:]
	}
	return c, nil
}

func (f *fakeSource) GPUs(context.Context) ([]GPU, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gpuCalls++
	return f.gpus, nil
}

func (f *fakeSource) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gpuCalls
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestSamplerDerivesRates(t *testing.T) {
	src := &fakeSource{counters: []Counters{
		{CPUBusy: 10, CPUTotal: 100, MemUsed: 4 << 30, MemTotal: 16 << 30, NetRx: 1000, NetTx: 500, ProcCPU: 1, ProcRSS: 100 << 20, Load: []float64{1, 2, 3}},
		// 8 cores x 2 s = 16 core-seconds elapsed; 4 busy; Ether used 2.
		{CPUBusy: 14, CPUTotal: 116, MemUsed: 5 << 30, MemTotal: 16 << 30, NetRx: 3000, NetTx: 4500, ProcCPU: 3, ProcRSS: 120 << 20},
		// Counter reset: no negative rates.
		{CPUBusy: 15, CPUTotal: 132, MemUsed: 5 << 30, MemTotal: 16 << 30, NetRx: 10, NetTx: 10, ProcCPU: 3},
	}}
	clk := &clock{t: time.Unix(1000, 0)}
	s := New(Options{Source: src, Interval: 2 * time.Second, History: 2, Now: clk.now})
	ctx := context.Background()
	s.Sample(ctx)
	if snap := s.Snapshot(); snap.Current != nil || len(snap.History) != 0 {
		t.Fatalf("one reading has no rates yet: %+v", snap)
	}
	clk.t = clk.t.Add(2 * time.Second)
	s.Sample(ctx)
	snap := s.Snapshot()
	c := snap.Current
	if c == nil || c.CPU != 25 || c.ProcCPU != 12.5 || c.NetRxBps != 1000 || c.NetTxBps != 2000 || c.MemUsed != 5<<30 || c.ProcRSS != 120<<20 {
		t.Fatalf("current: %+v", c)
	}
	if snap.MemTotal != 16<<30 || snap.IntervalSeconds != 2 {
		t.Fatalf("snapshot: %+v", snap)
	}
	clk.t = clk.t.Add(2 * time.Second)
	s.Sample(ctx)
	clk.t = clk.t.Add(2 * time.Second)
	s.Sample(ctx)
	snap = s.Snapshot()
	if len(snap.History) != 2 {
		t.Fatalf("history not bounded: %d", len(snap.History))
	}
	if r := snap.History[0]; r.NetRxBps != 0 || r.NetTxBps != 0 {
		t.Fatalf("counter reset produced a rate: %+v", r)
	}
}

func TestSamplerReportsErrors(t *testing.T) {
	src := &fakeSource{err: errors.New("no /proc")}
	s := New(Options{Source: src})
	s.Sample(context.Background())
	if snap := s.Snapshot(); snap.Error != "no /proc" || snap.History == nil {
		t.Fatalf("snapshot: %+v", snap)
	}
}

func TestGPUReadOnlyWhileViewed(t *testing.T) {
	util := 40.0
	src := &fakeSource{counters: []Counters{{CPUTotal: 1}}, gpus: []GPU{{Name: "RTX", Util: &util}}}
	clk := &clock{t: time.Unix(1000, 0)}
	s := New(Options{Source: src, Interval: time.Second, GPUInterval: 5 * time.Second, GPUIdle: time.Minute, Now: clk.now})
	ctx := context.Background()
	s.Sample(ctx)
	clk.t = clk.t.Add(2 * time.Minute)
	s.Sample(ctx)
	if src.calls() != 0 {
		t.Fatal("GPU read while nobody was looking")
	}
	s.Snapshot() // someone opens the Overview
	waitFor(t, func() bool { return src.calls() == 1 })
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.gpuBusy })
	clk.t = clk.t.Add(time.Second)
	s.Sample(ctx)
	if src.calls() != 1 {
		t.Fatal("GPU read again before GPUInterval")
	}
	clk.t = clk.t.Add(5 * time.Second)
	s.Sample(ctx)
	waitFor(t, func() bool { return src.calls() == 2 })
	snap := s.Snapshot()
	if len(snap.GPUs) != 1 || snap.Current == nil || snap.Current.GPU == nil || *snap.Current.GPU != 40 {
		t.Fatalf("snapshot: %+v", snap)
	}
	clk.t = clk.t.Add(2 * time.Minute)
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.gpuBusy })
	s.Sample(ctx)
	if src.calls() != 2 {
		t.Fatal("GPU kept reading after the page closed")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	gpus := parseNvidiaSMI([]byte("NVIDIA GeForce RTX 4080, 37, 12, 8, 2048, 16376\nTesla T4, [N/A], 0, 0, 100, 15360\n"))
	if len(gpus) != 2 {
		t.Fatalf("%+v", gpus)
	}
	g := gpus[0]
	if g.Name != "NVIDIA GeForce RTX 4080" || *g.Util != 37 || *g.Encoder != 12 || *g.Decoder != 8 || g.MemUsed != 2048<<20 || g.MemTotal != 16376<<20 {
		t.Fatalf("%+v", g)
	}
	if gpus[1].Util != nil {
		t.Fatal("[N/A] should be unknown")
	}
}

func TestParseIOReg(t *testing.T) {
	out := []byte(`+-o AGXAcceleratorG16X  <class AGXAcceleratorG16X, id 0x1000005c3, registered, matched, active, busy 0 (0 ms), retain 75>
    {
      "PerformanceStatistics" = {"In use system memory"=736395264,"Device Utilization %"=46,"Renderer Utilization %"=46}
      "model" = "Apple M4 Pro"
      "gpu-core-count" = 16
    }
`)
	gpus := parseIOReg(out)
	if len(gpus) != 1 || gpus[0].Name != "Apple M4 Pro" || *gpus[0].Util != 46 || gpus[0].MemUsed != 736395264 {
		t.Fatalf("%+v", gpus)
	}
}

func TestReadAMDSysfs(t *testing.T) {
	root := t.TempDir()
	write := func(rel, v string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(v+"\n"), 0o644)
	}
	write("card0/device/gpu_busy_percent", "63")
	write("card0/device/mem_info_vram_used", "1073741824")
	write("card0/device/mem_info_vram_total", "8589934592")
	write("card0-HDMI-A-1/status", "connected")
	write("card1/device/vendor", "0x8086") // Intel: no busy counter
	gpus := readAMDSysfs(root)
	if len(gpus) != 1 || gpus[0].Name != "AMD GPU" || *gpus[0].Util != 63 || gpus[0].MemTotal != 8<<30 {
		t.Fatalf("%+v", gpus)
	}
}

func TestPhysicalInterfaces(t *testing.T) {
	for _, n := range []string{"eth0", "en0", "enp3s0", "wlan0", "Ethernet", "Wi-Fi"} {
		if !physical(n) {
			t.Errorf("%s should count", n)
		}
	}
	for _, n := range []string{"lo", "lo0", "docker0", "veth12ab", "br-1234", "utun3", "wg0", "tailscale0", "vEthernet (WSL)", "Loopback Pseudo-Interface 1"} {
		if physical(n) {
			t.Errorf("%s should not count", n)
		}
	}
}

// TestHostSource reads this machine; it only checks the readings are sane.
func TestHostSource(t *testing.T) {
	c, err := hostSource{}.Counters(context.Background())
	if err != nil {
		t.Skipf("host counters unavailable here: %v", err)
	}
	if c.CPUTotal <= 0 || c.MemTotal == 0 || c.MemUsed > c.MemTotal || c.ProcRSS == 0 {
		t.Fatalf("implausible counters: %+v", c)
	}
}
