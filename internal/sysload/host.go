package sysload

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// hostSource reads the real machine.
type hostSource struct{}

func (hostSource) Counters(ctx context.Context) (Counters, error) {
	var c Counters
	times, err := cpu.TimesWithContext(ctx, false)
	if err != nil {
		return c, err
	}
	if len(times) == 0 {
		return c, errors.New("no CPU times reported")
	}
	t := times[0]
	c.CPUTotal = t.Total()
	c.CPUBusy = c.CPUTotal - t.Idle - t.Iowait

	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return c, err
	}
	c.MemTotal = vm.Total
	// Used means "not available": caches the OS would hand back count as free.
	if vm.Available <= vm.Total {
		c.MemUsed = vm.Total - vm.Available
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		c.SwapUsed, c.SwapTotal = sw.Used, sw.Total
	}

	if nics, err := net.IOCountersWithContext(ctx, true); err == nil {
		for _, n := range nics {
			if physical(n.Name) {
				c.NetRx += n.BytesRecv
				c.NetTx += n.BytesSent
			}
		}
	}

	if p, err := process.NewProcessWithContext(ctx, int32(os.Getpid())); err == nil {
		if pt, err := p.TimesWithContext(ctx); err == nil {
			c.ProcCPU = pt.User + pt.System
		}
		if mi, err := p.MemoryInfoWithContext(ctx); err == nil {
			c.ProcRSS = mi.RSS
		}
	}

	// Windows has no load average; gopsutil would emulate one with a
	// background counter we don't need.
	if runtime.GOOS != "windows" {
		if l, err := load.AvgWithContext(ctx); err == nil {
			c.Load = []float64{l.Load1, l.Load5, l.Load15}
		}
	}
	return c, nil
}

func (hostSource) GPUs(ctx context.Context) ([]GPU, error) { return readGPUs(ctx) }

// virtualNICs prefix interfaces whose traffic also crosses a physical one
// (loopback, container bridges, VPN tunnels), so counting them would
// double it.
var virtualNICs = []string{
	"lo", "veth", "docker", "br-", "virbr", "vmnet", "vboxnet", "bridge", "cni", "flannel",
	"cali", "kube", "tun", "tap", "utun", "wg", "tailscale", "zt", "awdl", "llw", "anpi", "gif", "stf",
	"isatap", "teredo", "vethernet", "loopback",
}

func physical(name string) bool {
	low := strings.ToLower(name)
	for _, p := range virtualNICs {
		if strings.HasPrefix(low, p) {
			return false
		}
	}
	return true
}
