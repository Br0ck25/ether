package sysload

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// readGPUs reports every GPU this platform can describe: NVIDIA through
// nvidia-smi (Linux and Windows), Apple GPUs through ioreg, and AMD on
// Linux through sysfs. Other adapters (Intel, AMD on Windows) have no
// unprivileged utilisation counter, so they are left out.
func readGPUs(ctx context.Context) ([]GPU, error) {
	var gpus []GPU
	if smi, err := exec.LookPath("nvidia-smi"); err == nil {
		out, err := exec.CommandContext(ctx, smi,
			"--query-gpu=name,utilization.gpu,utilization.encoder,utilization.decoder,memory.used,memory.total",
			"--format=csv,noheader,nounits").Output()
		if err == nil {
			gpus = append(gpus, parseNvidiaSMI(out)...)
		}
	}
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-r", "-d", "1", "-w", "0", "-c", "IOAccelerator").Output()
		if err == nil {
			gpus = append(gpus, parseIOReg(out)...)
		}
	case "linux":
		gpus = append(gpus, readAMDSysfs("/sys/class/drm")...)
	}
	return gpus, nil
}

// parseNvidiaSMI reads nvidia-smi's CSV (no header, no units); memory is
// in MiB and unsupported fields read "[N/A]".
func parseNvidiaSMI(out []byte) []GPU {
	var gpus []GPU
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 6 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		g := GPU{Name: f[0], Util: pct(f[1]), Encoder: pct(f[2]), Decoder: pct(f[3])}
		if v, err := strconv.ParseFloat(f[4], 64); err == nil {
			g.MemUsed = uint64(v) << 20
		}
		if v, err := strconv.ParseFloat(f[5], 64); err == nil {
			g.MemTotal = uint64(v) << 20
		}
		gpus = append(gpus, g)
	}
	return gpus
}

var (
	ioregModelRe = regexp.MustCompile(`"model" = "([^"]+)"`)
	ioregUtilRe  = regexp.MustCompile(`"Device Utilization %"=(\d+)`)
	ioregMemRe   = regexp.MustCompile(`"In use system memory"=(\d+)`)
)

// parseIOReg reads `ioreg -c IOAccelerator`: one block per accelerator,
// each starting with "+-o". Apple GPUs share system memory, so memory is
// what the GPU has in use without a total.
func parseIOReg(out []byte) []GPU {
	var gpus []GPU
	for _, block := range bytes.Split(out, []byte("+-o ")) {
		m := ioregUtilRe.FindSubmatch(block)
		if m == nil {
			continue
		}
		g := GPU{Name: "GPU", Util: pct(string(m[1]))}
		if n := ioregModelRe.FindSubmatch(block); n != nil {
			g.Name = string(n[1])
		}
		if n := ioregMemRe.FindSubmatch(block); n != nil {
			g.MemUsed, _ = strconv.ParseUint(string(n[1]), 10, 64)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// readAMDSysfs reads amdgpu's counters under /sys/class/drm/cardN/device.
func readAMDSysfs(root string) []GPU {
	cards, _ := filepath.Glob(filepath.Join(root, "card[0-9]*"))
	var gpus []GPU
	for _, card := range cards {
		if strings.Contains(filepath.Base(card), "-") { // connectors: card0-HDMI-A-1
			continue
		}
		dev := filepath.Join(card, "device")
		busy := pct(readTrim(filepath.Join(dev, "gpu_busy_percent")))
		if busy == nil {
			continue
		}
		g := GPU{Name: readTrim(filepath.Join(dev, "product_name")), Util: busy}
		if g.Name == "" {
			g.Name = "AMD GPU"
		}
		g.MemUsed, _ = strconv.ParseUint(readTrim(filepath.Join(dev, "mem_info_vram_used")), 10, 64)
		g.MemTotal, _ = strconv.ParseUint(readTrim(filepath.Join(dev, "mem_info_vram_total")), 10, 64)
		gpus = append(gpus, g)
	}
	return gpus
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func pct(s string) *float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return nil
	}
	v = clampPct(v)
	return &v
}
