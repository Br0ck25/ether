package remote

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestGDMBackoffGrowsAndCaps(t *testing.T) {
	var d time.Duration
	var seq []time.Duration
	for i := 0; i < 12; i++ {
		d = nextGDMBackoff(d)
		seq = append(seq, d)
	}
	if seq[0] != gdmErrBackoffMin || seq[1] != 2*gdmErrBackoffMin {
		t.Fatalf("start of sequence %v", seq[:2])
	}
	if seq[len(seq)-1] != gdmErrBackoffMax {
		t.Fatalf("not capped: %v", seq)
	}
}

func TestGDMRejoinKeepsAnswering(t *testing.T) {
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()

	info := GDMInfo{Name: "Ether", MachineIdentifier: "m", Port: 32400, Version: "1"}
	g := NewGDMResponder(GDMOptions{Info: func() GDMInfo { return info }, Ports: []int{port}, rejoinInterval: 100 * time.Millisecond})
	if err := g.Start(context.Background()); err != nil {
		t.Skipf("cannot bind GDM socket here: %v", err)
	}
	defer g.Stop()
	g.mu.Lock()
	first := g.conns[0]
	g.mu.Unlock()

	time.Sleep(350 * time.Millisecond)
	g.mu.Lock()
	current := g.conns[0]
	g.mu.Unlock()
	if current == first {
		t.Fatal("socket was not reopened")
	}
	reps, err := DiscoverGDM(context.Background(), fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil || len(reps) != 1 || reps[0].MachineIdentifier != "m" {
		t.Fatalf("after rejoin: %v %v", reps, err)
	}
}
