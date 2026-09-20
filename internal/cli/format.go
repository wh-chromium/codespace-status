package cli

import (
	"fmt"

	"github.com/wh-chromium/codespace-status/internal/metrics"
)

// formatSample renders one sample as a compact single line.
func formatSample(s metrics.Sample) string {
	return fmt.Sprintf("cpu %5.1f%%  mem %5.1f%% (%s/%s)  net in %s/s out %s/s  disk io %6.1f/s r %s/s w %s/s",
		s.CPUPercent, s.MemPercent,
		humanBytes(float64(s.MemUsedBytes)), humanBytes(float64(s.MemTotalBytes)),
		humanBytes(s.NetInBPS), humanBytes(s.NetOutBPS),
		s.DiskIOPS, humanBytes(s.DiskReadBPS), humanBytes(s.DiskWriteBPS))
}

// humanBytes renders a byte count with a binary unit suffix.
func humanBytes(n float64) string {
	const unit = 1024.0
	switch {
	case n < unit:
		return fmt.Sprintf("%.0fB", n)
	case n < unit*unit:
		return fmt.Sprintf("%.1fKB", n/unit)
	case n < unit*unit*unit:
		return fmt.Sprintf("%.1fMB", n/unit/unit)
	default:
		return fmt.Sprintf("%.2fGB", n/unit/unit/unit)
	}
}
