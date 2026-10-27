package boxd

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"boxaz/core/internal/proto"
)

// hostCapacity reads CPU count, RAM (MB) and disk (GB) of the filesystem holding dir.
func hostCapacity(dir string) (float64, int, int) {
	cpu := float64(runtime.NumCPU())
	ram := 0
	if total, _ := memInfo(); total > 0 {
		ram = int(total / 1024)
	}
	disk := 0
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) == nil {
		disk = int(st.Blocks * uint64(st.Bsize) >> 30)
	}
	return cpu, ram, disk
}

// memInfo returns MemTotal and MemAvailable in kB.
func memInfo() (uint64, uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var total, avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	return total, avail
}

func cpuTimes() (idle, total uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	f := strings.Fields(line)
	for i, s := range f[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		total += v
		if i == 3 || i == 4 {
			idle += v
		}
	}
	return
}

func (a *Agent) collectMetrics(ctx context.Context) proto.Metrics {
	var m proto.Metrics
	idle, total := cpuTimes()
	a.mu.Lock()
	pi, pt := a.cpuPrev[0], a.cpuPrev[1]
	a.cpuPrev = [2]uint64{idle, total}
	a.mu.Unlock()
	if pt > 0 && total > pt {
		m.CPUUsedPct = round1(100 * (1 - float64(idle-pi)/float64(total-pt)))
	}
	if t, av := memInfo(); t > 0 {
		m.RAMUsedPct = round1(100 * (1 - float64(av)/float64(t)))
	}
	var st syscall.Statfs_t
	if syscall.Statfs(a.cfg.StateDir, &st) == nil && st.Blocks > 0 {
		m.DiskUsedPct = round1(100 * (1 - float64(st.Bavail)/float64(st.Blocks)))
	}
	if ids, err := a.drv.List(ctx); err == nil {
		m.BoxCount = len(ids)
	}
	if u, ok := a.drv.(Usager); ok { // v1.3: per-box usage for right-sizing
		if bu := u.Usage(ctx); len(bu) > 0 {
			m.Boxes = map[string]proto.BoxUsage{}
			for id, v := range bu {
				m.Boxes[a.aliases.public(id)] = v
			}
		}
	}
	return m
}

// Usager reports per-box CPU (% of one vCPU since the last call) and RAM.
type Usager interface {
	Usage(ctx context.Context) map[string]proto.BoxUsage
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
