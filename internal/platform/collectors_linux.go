//go:build linux

package platform

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"rackops/agent/internal/collect"
)

func allCollectors() []collect.Collector {
	return []collect.Collector{
		&cpuCollector{},
		&memoryCollector{},
		&diskCollector{},
		&networkCollector{},
		&temperatureCollector{},
		&uptimeCollector{},
		&hardwareCollector{},
	}
}

// cpuCollector samples /proc/stat twice (keeps its last sample) and
// reports total CPU usage percent.
type cpuCollector struct {
	prev totalCPU
}

func (c *cpuCollector) ID() collect.ID { return collect.IDCPU }

func (c *cpuCollector) Collect(ctx context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	cur, err := readTotalCPU()
	if err != nil {
		return err
	}
	if c.prev.total == 0 {
		// First sample: no delta yet; report 0 but record for next time.
		c.prev = cur
		m.Cpu.UsagePercent = 0
		return nil
	}
	dt := float64(cur.total - c.prev.total)
	di := float64(cur.idle - c.prev.idle)
	c.prev = cur
	if dt <= 0 {
		m.Cpu.UsagePercent = 0
		return nil
	}
	m.Cpu.UsagePercent = 100 * (1 - di/dt)
	return nil
}

type totalCPU struct {
	total uint64
	idle  uint64
}

func readTotalCPU() (totalCPU, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return totalCPU{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return totalCPU{}, err
		}
		return totalCPU{}, fmt.Errorf("linux cpu: empty /proc/stat")
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return totalCPU{}, fmt.Errorf("linux cpu: unexpected /proc/stat first line %q", fields[0])
	}
	var total, idle uint64
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		total += v
		switch i {
		case 3: // idle
		case 4: // iowait counts as idle
			idle += v
		}
	}
	return totalCPU{total: total, idle: idle}, nil
}

// memoryCollector reads /proc/meminfo.
type memoryCollector struct{}

func (c *memoryCollector) ID() collect.ID { return collect.IDMemory }

func (c *memoryCollector) Collect(_ context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return err
	}
	fields := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.TrimSpace(parts[1])
		// values are "NNN kB"
		valStr = strings.TrimSuffix(valStr, " kB")
		valStr = strings.TrimSpace(valStr)
		v, err := strconv.ParseUint(valStr, 10, 64)
		if err != nil {
			continue
		}
		fields[key] = v * 1024
	}
	total, ok := fields["MemTotal"]
	if !ok {
		return fmt.Errorf("linux memory: MemTotal missing")
	}
	free := fields["MemAvailable"]
	if free == 0 {
		free = fields["MemFree"]
	}
	m.Memory.TotalBytes = total
	m.Memory.UsedBytes = total - free
	return nil
}

// diskCollector reports every real filesystem mounted from /proc/mounts,
// using statfs for sizes.
type diskCollector struct{}

func (c *diskCollector) ID() collect.ID { return collect.IDDisk }

func (c *diskCollector) Collect(_ context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var disks []collect.Disk
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		fstype := fields[2]
		mount := fields[1]
		// Skip pseudo/filesystems we don't want on a dashboard.
		switch fstype {
		case "proc", "sysfs", "devtmpfs", "devpts", "tmpfs", "cgroup",
			"cgroup2", "pstore", "securityfs", "debugfs", "tracefs",
			"configfs", "fusectl", "mqueue", "hugetlbfs", "autofs",
			"binfmt_misc", "nsfs", "overlay", "squashfs", "ramfs",
			"rpc_pipefs", "fuse.lxcfs":
			continue
		}
		if seen[mount] {
			continue
		}
		seen[mount] = true
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue
		}
		d := collect.Disk{
			MountPoint: mount,
			TotalBytes: st.Blocks * uint64(st.Bsize),
			FreeBytes:  st.Bavail * uint64(st.Bsize), // free for unprivileged users
			FSType:     fstype,
		}
		disks = append(disks, d)
	}
	m.Disks = disks
	return nil
}

// networkCollector sums byte counters across non-internal interfaces and
// reports rates (delta since last sample, divided by the elapsed time).
type networkCollector struct {
	prev   map[string]ifaceCounters
	prevAt time.Time
}

type ifaceCounters struct{ in, out uint64 }

func (c *networkCollector) ID() collect.ID { return collect.IDNetwork }

func (c *networkCollector) Collect(ctx context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return err
	}
	cur := map[string]ifaceCounters{}
	lines := strings.Split(string(data), "\n")
	// Skip the two header lines.
	for _, line := range lines[2:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		// lo and virtual interfaces don't represent host traffic.
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") {
			continue
		}
		in, _ := strconv.ParseUint(fields[0], 10, 64)
		out, _ := strconv.ParseUint(fields[8], 10, 64)
		cur[name] = ifaceCounters{in: in, out: out}
	}

	now := time.Now()
	if c.prev == nil {
		// First sample: there is no interval to divide by yet. Record
		// the baseline and report a zero rate rather than pretending
		// the cumulative counters are a rate.
		c.prev, c.prevAt = cur, now
		return nil
	}
	elapsed := now.Sub(c.prevAt).Seconds()
	var in, out uint64
	for name, cur2 := range cur {
		if p, ok := c.prev[name]; ok {
			if cur2.in >= p.in {
				in += cur2.in - p.in
			}
			if cur2.out >= p.out {
				out += cur2.out - p.out
			}
		}
	}
	c.prev, c.prevAt = cur, now
	if elapsed <= 0 {
		m.Network.BytesInPerSecond = 0
		m.Network.BytesOutPerSecond = 0
		return nil
	}
	m.Network.BytesInPerSecond = float64(in) / elapsed
	m.Network.BytesOutPerSecond = float64(out) / elapsed
	return nil
}

// temperatureCollector reads /sys/class/thermal/thermal_zone* and hwmon.
type temperatureCollector struct{}

func (c *temperatureCollector) ID() collect.ID { return collect.IPTemperature }

func (c *temperatureCollector) Collect(_ context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	zones, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return fmt.Errorf("linux temperature: %w", err)
	}
	var best *float64
	for _, z := range zones {
		if !strings.HasPrefix(z.Name(), "thermal_zone") {
			continue
		}
		data, err := os.ReadFile("/sys/class/thermal/" + z.Name() + "/temp")
		if err != nil {
			continue
		}
		// Millidegrees Celsius.
		v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil {
			continue
		}
		celsius := v / 1000
		if best == nil || celsius > *best {
			best = &celsius
		}
	}
	m.Temperature = best // nil when no sensor readable
	return nil
}

// uptimeCollector reads /proc/uptime.
type uptimeCollector struct{}

func (c *uptimeCollector) ID() collect.ID { return collect.IDUptime }

func (c *uptimeCollector) Collect(_ context.Context, m *collect.HostMetrics, info *collect.HostInfo) error {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return fmt.Errorf("linux uptime: empty /proc/uptime")
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return err
	}
	m.UptimeSeconds = sec
	info.Uptime = sec
	return nil
}

// hardwareCollector gathers the "specs": CPU model, CPU count, total memory,
// block devices, board/vendor (best effort from DMI).
type hardwareCollector struct{}

func (c *hardwareCollector) ID() collect.ID { return collect.IDHardware }

func (c *hardwareCollector) Collect(_ context.Context, m *collect.HostMetrics, info *collect.HostInfo) error {
	info.Hardware.NumCPU = numCPU()
	if s, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(s), "\n") {
			if v, ok := strings.CutPrefix(line, "model name"); ok {
				model := strings.TrimSpace(strings.TrimPrefix(v, ":"))
				if model != "" {
					info.Hardware.CPUModel = model
					break
				}
			}
		}
	}
	// Total memory from /proc/meminfo (reuses the memory collector's logic
	// inline to avoid coupling).
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						info.Hardware.MemoryTotalBytes = kb * 1024
					}
				}
				break
			}
		}
	}
	// Block devices from /sys/block (size in 512-byte sectors).
	for _, d := range blockDevices() {
		info.Hardware.Disks = append(info.Hardware.Disks, d)
	}
	// Board/vendor from DMI.
	if b := dmiString("/sys/class/dmi/id/sys_vendor"); b != "" {
		if p := dmiString("/sys/class/dmi/id/product_name"); p != "" {
			info.Hardware.Board = b + " " + p
		} else {
			info.Hardware.Board = b
		}
	}
	return nil
}

func numCPU() int {
	n := 0
	entries, err := os.ReadDir("/sys/devices/system/cpu")
	if err != nil {
		return n
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cpu") {
			n++
		}
	}
	return n
}

func blockDevices() []collect.HDDisk {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	var out []collect.HDDisk
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		base := "/sys/block/" + name
		size, _ := readUintFile(base + "/size") // in 512-byte sectors
		model := ""
		if f, err := os.ReadFile(base + "/device/model"); err == nil {
			model = strings.TrimSpace(string(f))
		}
		rot := true
		if f, err := os.ReadFile(base + "/queue/rotational"); err == nil {
			rot = strings.TrimSpace(string(f)) == "1"
		}
		out = append(out, collect.HDDisk{
			Name:  name,
			Model: model,
			Size:  size * 512,
			IsSSD: !rot,
		})
	}
	return out
}

func readUintFile(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func dmiString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if s == "Unknown" || s == "To Be Filled By O.E.M." {
		return ""
	}
	return s
}
