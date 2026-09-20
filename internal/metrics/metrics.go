// Package metrics parses Linux /proc counters sampled inside a codespace and
// converts consecutive snapshots into per-second resource usage samples.
package metrics

import (
	"bufio"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Block markers used by RemoteScript to frame one sample on the wire.
const (
	BeginMarker = "::CSBEGIN::"
	EndMarker   = "::CSEND::"
)

// RemoteScript builds the shell program run inside a codespace. It emits one
// framed block of /proc counters every interval, so a single SSH session
// serves every sample instead of reconnecting each time. The loop never reads
// stdin because a remote shell may buffer past a trigger line.
func RemoteScript(interval time.Duration) string {
	seconds := interval.Seconds()
	if seconds < 0.1 {
		seconds = 0.1
	}
	return fmt.Sprintf(`while :; do
  echo "%s"
  echo "::stat::"; cat /proc/stat
  echo "::meminfo::"; cat /proc/meminfo
  echo "::netdev::"; cat /proc/net/dev
  echo "::diskstats::"; cat /proc/diskstats
  echo "%s"
  sleep %s
done
`, BeginMarker, EndMarker, strconv.FormatFloat(seconds, 'f', -1, 64))
}

// Snapshot holds raw cumulative counters read at a point in time.
type Snapshot struct {
	Time time.Time

	CPUTotal uint64 // all jiffies across every state
	CPUIdle  uint64 // idle + iowait jiffies

	MemTotalBytes     uint64
	MemAvailableBytes uint64

	NetInBytes  uint64
	NetOutBytes uint64

	DiskReadBytes  uint64
	DiskWriteBytes uint64
	DiskReadOps    uint64
	DiskWriteOps   uint64
}

// Sample is a rendered, UI-facing measurement derived from two snapshots.
type Sample struct {
	TimeMS        int64   `json:"time_ms"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	MemTotalBytes uint64  `json:"mem_total_bytes"`
	MemPercent    float64 `json:"mem_percent"`
	NetInBPS      float64 `json:"net_in_bps"`
	NetOutBPS     float64 `json:"net_out_bps"`
	DiskReadBPS   float64 `json:"disk_read_bps"`
	DiskWriteBPS  float64 `json:"disk_write_bps"`
	DiskIOPS      float64 `json:"disk_iops"`
}

// ErrNoData reports a block that contained no usable counters.
var ErrNoData = errors.New("metrics: no counters found in sample block")

// sectorSize is the fixed /proc/diskstats unit, independent of hardware.
const sectorSize = 512

// Parse converts one framed block of /proc output into a Snapshot.
func Parse(block string, now time.Time) (Snapshot, error) {
	snap := Snapshot{Time: now}
	section := ""
	seen := false

	sc := bufio.NewScanner(strings.NewReader(block))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "::") && strings.HasSuffix(strings.TrimSpace(line), "::") {
			section = strings.Trim(strings.TrimSpace(line), ":")
			continue
		}
		switch section {
		case "stat":
			if parseStat(line, &snap) {
				seen = true
			}
		case "meminfo":
			if parseMeminfo(line, &snap) {
				seen = true
			}
		case "netdev":
			if parseNetDev(line, &snap) {
				seen = true
			}
		case "diskstats":
			if parseDiskstats(line, &snap) {
				seen = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Snapshot{}, err
	}
	if !seen {
		return Snapshot{}, ErrNoData
	}
	return snap, nil
}

// parseStat consumes the aggregate "cpu" line of /proc/stat.
func parseStat(line string, snap *Snapshot) bool {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return false
	}
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		snap.CPUTotal += v
		if i == 3 || i == 4 { // idle, iowait
			snap.CPUIdle += v
		}
	}
	return true
}

// parseMeminfo consumes MemTotal and MemAvailable (reported in kB).
func parseMeminfo(line string, snap *Snapshot) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false
	}
	v, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return false
	}
	switch fields[0] {
	case "MemTotal:":
		snap.MemTotalBytes = v * 1024
	case "MemAvailable:":
		snap.MemAvailableBytes = v * 1024
	default:
		return false
	}
	return true
}

// parseNetDev sums receive/transmit bytes across real interfaces.
func parseNetDev(line string, snap *Snapshot) bool {
	name, rest, ok := strings.Cut(line, ":")
	if !ok {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "docker") {
		return false
	}
	fields := strings.Fields(rest)
	if len(fields) < 9 {
		return false
	}
	in, err1 := strconv.ParseUint(fields[0], 10, 64)
	out, err2 := strconv.ParseUint(fields[8], 10, 64)
	if err1 != nil || err2 != nil {
		return false
	}
	snap.NetInBytes += in
	snap.NetOutBytes += out
	return true
}

// parseDiskstats sums whole-device I/O, skipping partitions and loop devices.
func parseDiskstats(line string, snap *Snapshot) bool {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return false
	}
	name := fields[2]
	if !wholeDevice(name) {
		return false
	}
	readOps, err1 := strconv.ParseUint(fields[3], 10, 64)
	readSectors, err2 := strconv.ParseUint(fields[5], 10, 64)
	writeOps, err3 := strconv.ParseUint(fields[7], 10, 64)
	writeSectors, err4 := strconv.ParseUint(fields[9], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return false
	}
	snap.DiskReadOps += readOps
	snap.DiskWriteOps += writeOps
	snap.DiskReadBytes += readSectors * sectorSize
	snap.DiskWriteBytes += writeSectors * sectorSize
	return true
}

// wholeDevice keeps sda/nvme0n1 style devices and drops partitions and loops.
func wholeDevice(name string) bool {
	switch {
	case strings.HasPrefix(name, "loop"), strings.HasPrefix(name, "ram"), strings.HasPrefix(name, "dm-"):
		return false
	}
	if strings.HasPrefix(name, "nvme") {
		return !strings.Contains(name, "p")
	}
	if len(name) > 0 && name[len(name)-1] >= '0' && name[len(name)-1] <= '9' {
		for _, p := range []string{"sd", "vd", "hd", "xvd"} {
			if strings.HasPrefix(name, p) {
				return false
			}
		}
	}
	return true
}

// Delta converts two snapshots into a Sample of per-second rates.
func Delta(prev, cur Snapshot) Sample {
	seconds := cur.Time.Sub(prev.Time).Seconds()
	if seconds <= 0 {
		seconds = 1
	}

	s := Sample{
		TimeMS:        cur.Time.UnixMilli(),
		MemTotalBytes: cur.MemTotalBytes,
	}
	if cur.MemTotalBytes > 0 {
		used := cur.MemTotalBytes
		if cur.MemAvailableBytes < cur.MemTotalBytes {
			used = cur.MemTotalBytes - cur.MemAvailableBytes
		}
		s.MemUsedBytes = used
		s.MemPercent = float64(used) / float64(cur.MemTotalBytes) * 100
	}

	totalDelta := sub(cur.CPUTotal, prev.CPUTotal)
	idleDelta := sub(cur.CPUIdle, prev.CPUIdle)
	if totalDelta > 0 {
		s.CPUPercent = clampPercent(float64(totalDelta-idleDelta) / float64(totalDelta) * 100)
	}

	s.NetInBPS = float64(sub(cur.NetInBytes, prev.NetInBytes)) / seconds
	s.NetOutBPS = float64(sub(cur.NetOutBytes, prev.NetOutBytes)) / seconds
	s.DiskReadBPS = float64(sub(cur.DiskReadBytes, prev.DiskReadBytes)) / seconds
	s.DiskWriteBPS = float64(sub(cur.DiskWriteBytes, prev.DiskWriteBytes)) / seconds
	ops := sub(cur.DiskReadOps, prev.DiskReadOps) + sub(cur.DiskWriteOps, prev.DiskWriteOps)
	s.DiskIOPS = float64(ops) / seconds
	return s
}

// sub subtracts counters while tolerating resets and wraparound.
func sub(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// clampPercent keeps CPU readings inside a sane 0-100 range.
func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
