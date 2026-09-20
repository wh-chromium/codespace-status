package metrics

import (
	"strings"
	"testing"
	"time"
)

// block builds a framed sample with the given per-section bodies.
func block(stat, meminfo, netdev, diskstats string) string {
	return strings.Join([]string{
		"::stat::", stat,
		"::meminfo::", meminfo,
		"::netdev::", netdev,
		"::diskstats::", diskstats,
	}, "\n")
}

const sampleMem = `MemTotal:        8000000 kB
MemFree:         1000000 kB
MemAvailable:    4000000 kB`

const sampleNet = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets
    lo: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0
  eth0: 5000 50 0 0 0 0 0 0 7000 70 0 0 0 0 0 0`

const sampleDisk = `   8       0 sda 100 0 200 0 300 0 400 0 0 0 0
   8       1 sda1 50 0 100 0 150 0 200 0 0 0 0
   7       0 loop0 9 0 9 0 9 0 9 0 0 0 0`

func TestParseReadsEverySection(t *testing.T) {
	snap, err := Parse(block("cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 1 2 3 4", sampleMem, sampleNet, sampleDisk), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if snap.CPUTotal != 1000 {
		t.Errorf("CPUTotal = %d, want 1000", snap.CPUTotal)
	}
	if snap.CPUIdle != 850 {
		t.Errorf("CPUIdle = %d, want 850", snap.CPUIdle)
	}
	if snap.MemTotalBytes != 8000000*1024 || snap.MemAvailableBytes != 4000000*1024 {
		t.Errorf("memory = %d/%d", snap.MemAvailableBytes, snap.MemTotalBytes)
	}
	if snap.NetInBytes != 5000 || snap.NetOutBytes != 7000 {
		t.Errorf("net = %d/%d, want 5000/7000 (lo excluded)", snap.NetInBytes, snap.NetOutBytes)
	}
	if snap.DiskReadOps != 100 || snap.DiskWriteOps != 300 {
		t.Errorf("disk ops = %d/%d, want 100/300 (partitions and loops excluded)", snap.DiskReadOps, snap.DiskWriteOps)
	}
	if snap.DiskReadBytes != 200*512 || snap.DiskWriteBytes != 400*512 {
		t.Errorf("disk bytes = %d/%d", snap.DiskReadBytes, snap.DiskWriteBytes)
	}
}

func TestParseEmptyBlock(t *testing.T) {
	if _, err := Parse("", time.Now()); err != ErrNoData {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
}

func TestDeltaComputesRates(t *testing.T) {
	start := time.Unix(100, 0)
	prev := Snapshot{
		Time: start, CPUTotal: 1000, CPUIdle: 900,
		MemTotalBytes: 1000, MemAvailableBytes: 600,
		NetInBytes: 1000, NetOutBytes: 2000,
		DiskReadBytes: 1024, DiskWriteBytes: 2048, DiskReadOps: 5, DiskWriteOps: 5,
	}
	cur := Snapshot{
		Time: start.Add(2 * time.Second), CPUTotal: 2000, CPUIdle: 1400,
		MemTotalBytes: 1000, MemAvailableBytes: 250,
		NetInBytes: 3000, NetOutBytes: 2400,
		DiskReadBytes: 3072, DiskWriteBytes: 2048, DiskReadOps: 15, DiskWriteOps: 5,
	}
	s := Delta(prev, cur)

	if s.CPUPercent != 50 {
		t.Errorf("CPUPercent = %v, want 50", s.CPUPercent)
	}
	if s.MemUsedBytes != 750 || s.MemPercent != 75 {
		t.Errorf("mem = %d (%v%%), want 750 (75%%)", s.MemUsedBytes, s.MemPercent)
	}
	if s.NetInBPS != 1000 || s.NetOutBPS != 200 {
		t.Errorf("net = %v/%v, want 1000/200", s.NetInBPS, s.NetOutBPS)
	}
	if s.DiskReadBPS != 1024 || s.DiskWriteBPS != 0 {
		t.Errorf("disk bytes = %v/%v, want 1024/0", s.DiskReadBPS, s.DiskWriteBPS)
	}
	if s.DiskIOPS != 5 {
		t.Errorf("DiskIOPS = %v, want 5", s.DiskIOPS)
	}
	if s.TimeMS != cur.Time.UnixMilli() {
		t.Errorf("TimeMS = %d", s.TimeMS)
	}
}

func TestDeltaToleratesCounterResets(t *testing.T) {
	start := time.Unix(0, 0)
	prev := Snapshot{Time: start, NetInBytes: 5000, CPUTotal: 10, CPUIdle: 5}
	cur := Snapshot{Time: start.Add(time.Second), NetInBytes: 10, CPUTotal: 5, CPUIdle: 1}
	s := Delta(prev, cur)
	if s.NetInBPS != 0 {
		t.Errorf("NetInBPS = %v, want 0 after reset", s.NetInBPS)
	}
	if s.CPUPercent != 0 {
		t.Errorf("CPUPercent = %v, want 0 after reset", s.CPUPercent)
	}
}

func TestRemoteScriptUsesInterval(t *testing.T) {
	script := RemoteScript(1500 * time.Millisecond)
	if !strings.Contains(script, "sleep 1.5") {
		t.Errorf("script missing interval:\n%s", script)
	}
	if !strings.Contains(script, BeginMarker) || !strings.Contains(script, EndMarker) {
		t.Error("script missing frame markers")
	}
	if strings.Contains(script, "read ") {
		t.Error("script must not read stdin")
	}
	if !strings.Contains(RemoteScript(0), "sleep 0.1") {
		t.Error("interval not clamped to a minimum")
	}
}

func TestWholeDevice(t *testing.T) {
	cases := map[string]bool{
		"sda": true, "sda1": false, "nvme0n1": true, "nvme0n1p3": false,
		"loop0": false, "dm-0": false, "vda": true, "vda2": false, "xvdb1": false,
	}
	for name, want := range cases {
		if got := wholeDevice(name); got != want {
			t.Errorf("wholeDevice(%q) = %v, want %v", name, got, want)
		}
	}
}
