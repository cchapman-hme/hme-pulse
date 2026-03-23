// Package rightsizing provides P95-based VM and container right-sizing analysis.
// It reads existing metrics from the SQLite store and classifies each running
// guest without introducing new goroutines, tables, or external dependencies.
package rightsizing

import (
	"math"
	"time"

	"github.com/rcourtman/pulse-go-rewrite/internal/models"
	"github.com/rcourtman/pulse-go-rewrite/pkg/metrics"
)

// MetricsQuerier abstracts read access to the metrics store for testing.
// It uses QueryAll to fetch all metric types for a guest in a single SQLite
// round-trip instead of one query per metric type.
type MetricsQuerier interface {
	QueryAll(resourceType, resourceID string,
		start, end time.Time, stepSecs int64) (map[string][]metrics.MetricPoint, error)
}

// GuestResult holds the right-sizing analysis for a single guest.
type GuestResult struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Node          string  `json:"node"`
	Type          string  `json:"type"` // "vm" or "container"
	VMID          int     `json:"vmid"`
	Status        string  `json:"status"`
	CPUs          int     `json:"cpus"`
	MaxMemBytes   int64   `json:"maxMemBytes"`
	CPUAvg        float64 `json:"cpuAvg"`
	CPUP95        float64 `json:"cpuP95"`
	CPUMax        float64 `json:"cpuMax"`
	MemAvg        float64 `json:"memAvg"`
	MemP95        float64 `json:"memP95"`
	MemMax        float64 `json:"memMax"`
	CPUVerdict    Verdict `json:"cpuVerdict"`
	MemVerdict    Verdict `json:"memVerdict"`
	Overall       Verdict `json:"overall"`
	DaysAtVerdict int     `json:"daysAtVerdict"`
	SampleCount   int     `json:"sampleCount"`
}

// Summary holds aggregate statistics across all analyzed guests.
type Summary struct {
	TotalGuests      int     `json:"totalGuests"`
	Idle             int     `json:"idle"`
	OverProvisioned  int     `json:"overProvisioned"`
	RightSized       int     `json:"rightSized"`
	UnderProvisioned int     `json:"underProvisioned"`
	Mixed            int     `json:"mixed"`
	InsufficientData int     `json:"insufficientData"`
	ReclaimableMemGB float64 `json:"reclaimableMemoryGB"`
	ReclaimableCPUs  int     `json:"reclaimableCPUs"`
}

// Result is the complete API response for a right-sizing analysis run.
type Result struct {
	Summary         Summary       `json:"summary"`
	Guests          []GuestResult `json:"guests"`
	Range           string        `json:"range"`
	Tier            string        `json:"tier"`
	DataQuality     string        `json:"dataQuality"`
	DataQualityNote string        `json:"dataQualityNote,omitempty"`
	ComputeTimeMs   int64         `json:"computeTimeMs"`
}

// rangeConfig maps user-facing range strings to query parameters.
type rangeConfig struct {
	Duration    time.Duration
	StepSecs    int64  // 0 means let the store choose the appropriate tier
	Tier        string // human-readable tier label
	DataQuality string // "high", "good", or "low"
	Note        string // optional explanatory note for the caller
}

// rangeConfigs defines the supported time ranges and their query parameters.
// StepSecs=0 for daily-tier ranges (7d, 14d, 30d) delegates tier selection to the store.
var rangeConfigs = map[string]rangeConfig{
	"1h":  {time.Hour, 60, "minute", "high", ""},
	"6h":  {6 * time.Hour, 60, "minute", "high", ""},
	"24h": {24 * time.Hour, 60, "minute", "high", ""},
	"7d":  {7 * 24 * time.Hour, 0, "hourly", "good", ""},
	"14d": {14 * 24 * time.Hour, 0, "daily", "low",
		"Based on daily averages — verdicts are directional, not precise"},
	"30d": {30 * 24 * time.Hour, 0, "daily", "low",
		"Based on daily averages — verdicts are directional, not precise"},
}

// Analyze performs right-sizing analysis on all running, non-template guests in the
// provided state snapshot. Templates are skipped because they are never actual workloads.
// Returns ErrInvalidRange only if timeRange is completely unrecognised; an unknown range
// falls back to "7d" to keep the caller simple.
func Analyze(querier MetricsQuerier, state models.StateSnapshot, t Thresholds, timeRange string) (*Result, error) {
	clockStart := time.Now()

	rc, ok := rangeConfigs[timeRange]
	if !ok {
		rc = rangeConfigs["7d"] // default
		timeRange = "7d"
	}

	end := time.Now()
	queryStart := end.Add(-rc.Duration)

	// Streak computation is only meaningful over multi-day spans.
	// For sub-day ranges (1h/6h/24h) it would require an extra 90-day query
	// per guest, adding unnecessary SQLite load for no analytical value.
	doStreak := rc.Duration >= 7*24*time.Hour

	var guests []GuestResult

	// Process VMs — skip stopped and templates.
	for _, vm := range state.VMs {
		if vm.Status != "running" || vm.Template {
			continue
		}
		gr := analyzeGuest(querier, vm.ID, vm.Name, vm.Node, "vm", vm.VMID,
			vm.CPUs, vm.Memory.Total, queryStart, end, rc.StepSecs, t, doStreak)
		guests = append(guests, gr)
	}

	// Process Containers — skip stopped and templates.
	for _, ct := range state.Containers {
		if ct.Status != "running" || ct.Template {
			continue
		}
		gr := analyzeGuest(querier, ct.ID, ct.Name, ct.Node, "container", ct.VMID,
			ct.CPUs, ct.Memory.Total, queryStart, end, rc.StepSecs, t, doStreak)
		guests = append(guests, gr)
	}

	// Build summary counters.
	summary := Summary{TotalGuests: len(guests)}
	for i := range guests {
		g := &guests[i]
		switch g.Overall {
		case VerdictIdle:
			summary.Idle++
		case VerdictOverProvisioned:
			summary.OverProvisioned++
		case VerdictRightSized:
			summary.RightSized++
		case VerdictUnderProvisioned:
			summary.UnderProvisioned++
		case VerdictMixed:
			summary.Mixed++
		case VerdictInsufficientData:
			summary.InsufficientData++
		}

		// Reclaimable memory: 20% headroom above P95 before treating remainder as reclaimable.
		// This matches DESIGN.md §5.3 — keep a safety buffer so the recommendation is
		// conservative rather than aggressive.
		if (g.MemVerdict == VerdictOverProvisioned || g.MemVerdict == VerdictIdle) &&
			g.MaxMemBytes > 0 && g.MemP95 > 0 {
			usedBytes := float64(g.MaxMemBytes) * (g.MemP95 / 100.0)
			reclaimable := (float64(g.MaxMemBytes) - usedBytes) * 0.8
			if reclaimable > 0 {
				summary.ReclaimableMemGB += reclaimable / (1024 * 1024 * 1024)
			}
		}

		// Reclaimable CPUs: estimate cores needed at 50% utilisation ceiling,
		// then subtract from the currently-allocated count.
		if (g.CPUVerdict == VerdictOverProvisioned || g.CPUVerdict == VerdictIdle) &&
			g.CPUs > 0 && g.CPUP95 > 0 {
			needed := int(math.Ceil(float64(g.CPUs) * (g.CPUP95 / 100.0) / 0.50))
			if needed < 1 {
				needed = 1
			}
			reclaimable := g.CPUs - needed
			if reclaimable > 0 {
				summary.ReclaimableCPUs += reclaimable
			}
		}
	}

	// Round memory to one decimal place for a cleaner UI display.
	summary.ReclaimableMemGB = math.Round(summary.ReclaimableMemGB*10) / 10

	elapsed := time.Since(clockStart).Milliseconds()

	return &Result{
		Summary:         summary,
		Guests:          guests,
		Range:           timeRange,
		Tier:            rc.Tier,
		DataQuality:     rc.DataQuality,
		DataQualityNote: rc.Note,
		ComputeTimeMs:   elapsed,
	}, nil
}

// analyzeGuest classifies a single guest by querying its CPU and memory metrics,
// computing P95 statistics, and combining the per-dimension verdicts.
func analyzeGuest(
	querier MetricsQuerier,
	id, name, node, guestType string,
	vmid, cpus int,
	maxMemBytes int64,
	start, end time.Time,
	stepSecs int64,
	t Thresholds,
	doStreak bool,
) GuestResult {
	gr := GuestResult{
		ID:          id,
		Name:        name,
		Node:        node,
		Type:        guestType,
		VMID:        vmid,
		Status:      "running",
		CPUs:        cpus,
		MaxMemBytes: maxMemBytes,
	}

	// A single QueryAll call fetches both cpu and memory in one SQLite round-trip.
	allPoints, err := querier.QueryAll("guest", id, start, end, stepSecs)
	if err != nil {
		gr.Overall = VerdictInsufficientData
		gr.CPUVerdict = VerdictInsufficientData
		gr.MemVerdict = VerdictInsufficientData
		return gr
	}
	cpuPoints := allPoints["cpu"]
	memPoints := allPoints["memory"]

	// Use the shorter series as the sample count for the MinSamples check —
	// if either dimension lacks data we cannot produce a reliable classification.
	sampleCount := len(cpuPoints)
	if len(memPoints) < sampleCount {
		sampleCount = len(memPoints)
	}
	gr.SampleCount = sampleCount

	if sampleCount < t.MinSamples {
		gr.Overall = VerdictInsufficientData
		gr.CPUVerdict = VerdictInsufficientData
		gr.MemVerdict = VerdictInsufficientData
		return gr
	}

	// Extract raw float64 slices for ComputeStats.
	cpuValues := make([]float64, len(cpuPoints))
	for i, p := range cpuPoints {
		cpuValues[i] = p.Value
	}
	memValues := make([]float64, len(memPoints))
	for i, p := range memPoints {
		memValues[i] = p.Value
	}

	cpuStats := ComputeStats(cpuValues)
	memStats := ComputeStats(memValues)

	// Round to 2 decimal places to avoid floating-point noise in JSON output.
	gr.CPUAvg = math.Round(cpuStats.Avg*100) / 100
	gr.CPUP95 = math.Round(cpuStats.P95*100) / 100
	gr.CPUMax = math.Round(cpuStats.Max*100) / 100
	gr.MemAvg = math.Round(memStats.Avg*100) / 100
	gr.MemP95 = math.Round(memStats.P95*100) / 100
	gr.MemMax = math.Round(memStats.Max*100) / 100

	gr.CPUVerdict = ClassifyCPU(cpuStats.P95, t)
	gr.MemVerdict = ClassifyMemory(memStats.P95, t)
	gr.Overall = ClassifyOverall(gr.CPUVerdict, gr.MemVerdict)

	// Streak is gated by the caller; skip for short ranges to avoid 90-day lookback queries.
	if doStreak {
		gr.DaysAtVerdict = streakDays(querier, id, gr.Overall, t)
	}

	return gr
}

// streakDays counts consecutive recent days where the guest held the given verdict,
// using daily (stepSecs=86400) metrics for the last 90 days.
// Returns 0 when currentVerdict is VerdictInsufficientData or data is unavailable.
func streakDays(querier MetricsQuerier, guestID string, currentVerdict Verdict, t Thresholds) int {
	if currentVerdict == VerdictInsufficientData {
		return 0
	}

	end := time.Now()
	start := end.Add(-90 * 24 * time.Hour)

	// One QueryAll call fetches both cpu and memory daily aggregates.
	allDaily, err := querier.QueryAll("guest", guestID, start, end, 86400)
	if err != nil {
		return 0
	}
	cpuPoints := allDaily["cpu"]
	memPoints := allDaily["memory"]
	if len(cpuPoints) == 0 || len(memPoints) == 0 {
		return 0
	}

	// Build date-keyed maps so we can look up any day in O(1).
	cpuByDate := make(map[string]float64, len(cpuPoints))
	for _, p := range cpuPoints {
		key := p.Timestamp.Format("2006-01-02")
		cpuByDate[key] = p.Value
	}
	memByDate := make(map[string]float64, len(memPoints))
	for _, p := range memPoints {
		key := p.Timestamp.Format("2006-01-02")
		memByDate[key] = p.Value
	}

	// Walk backwards from yesterday; break on the first day that differs.
	streak := 0
	for day := 1; day <= 90; day++ {
		date := end.AddDate(0, 0, -day)
		dateKey := date.Format("2006-01-02")

		cpuVal, cpuOk := cpuByDate[dateKey]
		memVal, memOk := memByDate[dateKey]
		if !cpuOk || !memOk {
			break // gap in data — streak ends
		}

		dayVerdict := ClassifyOverall(
			ClassifyCPU(cpuVal, t),
			ClassifyMemory(memVal, t),
		)
		if dayVerdict != currentVerdict {
			break
		}
		streak++
	}

	return streak
}
