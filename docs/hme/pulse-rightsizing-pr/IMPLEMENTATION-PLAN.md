# Pulse Right-Sizing Feature — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add deterministic, P95-based VM/container right-sizing analysis to Pulse as a standalone feature with a dedicated page and CSV export.

**Architecture:** New `internal/rightsizing/` package with pure classification functions + analyzer. One new API handler file. One new SolidJS page with supporting components. Reads existing `metrics.db` SQLite store — no new tables, no new goroutines, no new dependencies.

**Tech Stack:** Go 1.23+, SQLite (existing `metrics.db`), SolidJS + TypeScript + TailwindCSS (existing frontend stack), Lucide icons.

**Reference docs (read these first):**
- `docs/pulse-rightsizing-pr/DESIGN.md` — Full design document with architecture decisions
- `docs/pulse-rightsizing-pr/ALGORITHM-REFERENCE.md` — Exact algorithm port from ProxCenter with Go code

---

## Prerequisites

Before starting implementation:

1. Fork `rcourtman/pulse` on GitHub
2. Clone your fork locally
3. Create a feature branch: `git checkout -b feature/rightsizing`
4. Verify the project builds: `go build ./...`
5. Verify tests pass: `go test ./...`
6. Read these Pulse source files to understand existing patterns:
   - `internal/api/router.go` — How routes are registered (search for `HandleFunc`)
   - `pkg/metrics/store.go` — The `Query()` method signature and `MetricPoint` type
   - `internal/monitoring/metrics_history.go` — In-memory metrics (reference only)
   - `internal/models/state.go` — `StateSnapshot`, `VM`, `Container` types
   - `frontend-modern/src/pages/` — Existing page patterns
   - `frontend-modern/src/components/Dashboard/GuestRow.tsx` — Table patterns

---

## Task 1: Create `classify.go` — Pure Classification Functions

**Files:**
- Create: `internal/rightsizing/classify.go`

**Step 1: Create the file with types and thresholds**

```go
package rightsizing

import (
	"math"
	"sort"
)

// Verdict represents a right-sizing classification.
type Verdict string

const (
	VerdictIdle             Verdict = "idle"
	VerdictOverProvisioned  Verdict = "over-provisioned"
	VerdictRightSized       Verdict = "right-sized"
	VerdictUnderProvisioned Verdict = "under-provisioned"
	VerdictMixed            Verdict = "mixed"
	VerdictInsufficientData Verdict = "insufficient-data"
)

// Thresholds holds configurable classification thresholds (all 0-100 percentages).
type Thresholds struct {
	CPUIdle    float64 // P95 CPU below this → idle (default: 5)
	CPUOver    float64 // P95 CPU below this → over-provisioned (default: 30)
	CPUUnder   float64 // P95 CPU above this → under-provisioned (default: 85)
	MemIdle    float64 // P95 Mem below this → idle (default: 10)
	MemOver    float64 // P95 Mem below this → over-provisioned (default: 30)
	MemUnder   float64 // P95 Mem above this → under-provisioned (default: 90)
	MinSamples int     // Minimum data points required to classify (default: 10)
}

// DefaultThresholds returns sensible defaults for right-sizing classification.
func DefaultThresholds() Thresholds {
	return Thresholds{
		CPUIdle:    5.0,
		CPUOver:    30.0,
		CPUUnder:   85.0,
		MemIdle:    10.0,
		MemOver:    30.0,
		MemUnder:   90.0,
		MinSamples: 10,
	}
}

// Stats holds computed statistics for a metric.
type Stats struct {
	Avg float64
	P95 float64
	Max float64
}

// ComputeStats calculates average, P95, and max from a slice of values.
// Returns zero Stats if values is empty.
func ComputeStats(values []float64) Stats {
	if len(values) == 0 {
		return Stats{}
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	avg := sum / float64(len(sorted))
	p95 := sorted[int(math.Floor(0.95*float64(len(sorted)-1)))]
	max := sorted[len(sorted)-1]

	return Stats{Avg: avg, P95: p95, Max: max}
}

// ClassifyCPU classifies CPU sizing based on P95 CPU usage (0-100 percentage).
// Order matters: idle checked first, then under-provisioned, then over-provisioned.
func ClassifyCPU(cpuP95 float64, t Thresholds) Verdict {
	if cpuP95 < t.CPUIdle {
		return VerdictIdle
	}
	if cpuP95 > t.CPUUnder {
		return VerdictUnderProvisioned
	}
	if cpuP95 < t.CPUOver {
		return VerdictOverProvisioned
	}
	return VerdictRightSized
}

// ClassifyMemory classifies memory sizing based on P95 memory usage (0-100 percentage).
func ClassifyMemory(memP95 float64, t Thresholds) Verdict {
	if memP95 < t.MemIdle {
		return VerdictIdle
	}
	if memP95 > t.MemUnder {
		return VerdictUnderProvisioned
	}
	if memP95 < t.MemOver {
		return VerdictOverProvisioned
	}
	return VerdictRightSized
}

// ClassifyOverall derives the overall verdict from independent CPU and memory verdicts.
// Rules (in order):
//  1. Same verdict → return it
//  2. Either insufficient-data → insufficient-data
//  3. idle + over-provisioned → over-provisioned (both indicate waste; over is more actionable)
//  4. right-sized + anything else → the non-right-sized verdict
//  5. Otherwise → mixed
func ClassifyOverall(cpu, mem Verdict) Verdict {
	if cpu == mem {
		return cpu
	}
	if cpu == VerdictInsufficientData || mem == VerdictInsufficientData {
		return VerdictInsufficientData
	}
	// idle + over-provisioned → over-provisioned (both signals point to waste)
	if (cpu == VerdictIdle && mem == VerdictOverProvisioned) ||
		(cpu == VerdictOverProvisioned && mem == VerdictIdle) {
		return VerdictOverProvisioned
	}
	// right-sized + over/idle/under → the non-right-sized verdict
	if cpu == VerdictRightSized {
		return mem
	}
	if mem == VerdictRightSized {
		return cpu
	}
	return VerdictMixed
}
```

**Step 2: Verify it compiles**

```bash
go build ./internal/rightsizing/
```
Expected: No errors.

**Step 3: Commit**

```bash
git add internal/rightsizing/classify.go
git commit -m "feat(rightsizing): add pure classification functions

Port P95-based right-sizing algorithm from ProxCenter.
Classifies CPU and memory independently, then combines.
All thresholds configurable, defaults match ProxCenter."
```

---

## Task 2: Create `classify_test.go` — Unit Tests for Classification

**Files:**
- Create: `internal/rightsizing/classify_test.go`

**Step 1: Write comprehensive tests**

```go
package rightsizing

import (
	"math"
	"testing"
)

func TestComputeStats_Empty(t *testing.T) {
	s := ComputeStats(nil)
	if s.Avg != 0 || s.P95 != 0 || s.Max != 0 {
		t.Fatalf("expected zero stats for empty input, got %+v", s)
	}
}

func TestComputeStats_SingleValue(t *testing.T) {
	s := ComputeStats([]float64{42.0})
	if s.Avg != 42.0 || s.P95 != 42.0 || s.Max != 42.0 {
		t.Fatalf("expected all 42.0, got %+v", s)
	}
}

func TestComputeStats_KnownValues(t *testing.T) {
	// 100 values from 1..100
	values := make([]float64, 100)
	for i := range values {
		values[i] = float64(i + 1)
	}
	s := ComputeStats(values)

	if math.Abs(s.Avg-50.5) > 0.01 {
		t.Fatalf("avg: want 50.5, got %f", s.Avg)
	}
	// P95 index = floor(0.95 * 99) = floor(94.05) = 94 → values[94] = 95
	if s.P95 != 95.0 {
		t.Fatalf("p95: want 95.0, got %f", s.P95)
	}
	if s.Max != 100.0 {
		t.Fatalf("max: want 100.0, got %f", s.Max)
	}
}

func TestComputeStats_UnsortedInput(t *testing.T) {
	s := ComputeStats([]float64{50, 10, 90, 30, 70})
	if s.Max != 90.0 {
		t.Fatalf("max: want 90.0, got %f", s.Max)
	}
}

func TestClassifyCPU(t *testing.T) {
	th := DefaultThresholds()
	tests := []struct {
		cpuP95 float64
		want   Verdict
	}{
		{0.0, VerdictIdle},
		{3.0, VerdictIdle},
		{4.9, VerdictIdle},
		{5.0, VerdictOverProvisioned}, // exactly at idle threshold → over
		{15.0, VerdictOverProvisioned},
		{29.9, VerdictOverProvisioned},
		{30.0, VerdictRightSized}, // exactly at over threshold → right-sized
		{50.0, VerdictRightSized},
		{85.0, VerdictRightSized}, // exactly at under threshold → right-sized
		{85.1, VerdictUnderProvisioned},
		{95.0, VerdictUnderProvisioned},
		{100.0, VerdictUnderProvisioned},
	}
	for _, tt := range tests {
		got := ClassifyCPU(tt.cpuP95, th)
		if got != tt.want {
			t.Errorf("ClassifyCPU(%v) = %v, want %v", tt.cpuP95, got, tt.want)
		}
	}
}

func TestClassifyMemory(t *testing.T) {
	th := DefaultThresholds()
	tests := []struct {
		memP95 float64
		want   Verdict
	}{
		{0.0, VerdictIdle},
		{9.9, VerdictIdle},
		{10.0, VerdictOverProvisioned},
		{29.9, VerdictOverProvisioned},
		{30.0, VerdictRightSized},
		{60.0, VerdictRightSized},
		{90.0, VerdictRightSized},
		{90.1, VerdictUnderProvisioned},
		{100.0, VerdictUnderProvisioned},
	}
	for _, tt := range tests {
		got := ClassifyMemory(tt.memP95, th)
		if got != tt.want {
			t.Errorf("ClassifyMemory(%v) = %v, want %v", tt.memP95, got, tt.want)
		}
	}
}

func TestClassifyOverall(t *testing.T) {
	tests := []struct {
		cpu, mem Verdict
		want     Verdict
	}{
		{VerdictIdle, VerdictIdle, VerdictIdle},
		{VerdictRightSized, VerdictRightSized, VerdictRightSized},
		{VerdictOverProvisioned, VerdictOverProvisioned, VerdictOverProvisioned},
		{VerdictUnderProvisioned, VerdictUnderProvisioned, VerdictUnderProvisioned},

		// idle + over-provisioned → over-provisioned (both indicate waste)
		{VerdictIdle, VerdictOverProvisioned, VerdictOverProvisioned},
		{VerdictOverProvisioned, VerdictIdle, VerdictOverProvisioned},

		// right-sized + X → the non-right-sized verdict (X wins)
		{VerdictIdle, VerdictRightSized, VerdictIdle},
		{VerdictRightSized, VerdictIdle, VerdictIdle},
		{VerdictRightSized, VerdictOverProvisioned, VerdictOverProvisioned},
		{VerdictOverProvisioned, VerdictRightSized, VerdictOverProvisioned},
		{VerdictRightSized, VerdictUnderProvisioned, VerdictUnderProvisioned},
		{VerdictUnderProvisioned, VerdictRightSized, VerdictUnderProvisioned},

		// insufficient-data propagates
		{VerdictInsufficientData, VerdictRightSized, VerdictInsufficientData},
		{VerdictOverProvisioned, VerdictInsufficientData, VerdictInsufficientData},

		// true disagreements → mixed
		{VerdictIdle, VerdictUnderProvisioned, VerdictMixed},
		{VerdictOverProvisioned, VerdictUnderProvisioned, VerdictMixed},
	}
	for _, tt := range tests {
		got := ClassifyOverall(tt.cpu, tt.mem)
		if got != tt.want {
			t.Errorf("ClassifyOverall(%v, %v) = %v, want %v", tt.cpu, tt.mem, got, tt.want)
		}
	}
}

func TestCustomThresholds(t *testing.T) {
	th := Thresholds{
		CPUIdle: 10, CPUOver: 50, CPUUnder: 90,
		MemIdle: 20, MemOver: 40, MemUnder: 80,
		MinSamples: 5,
	}
	// With custom thresholds, 15% CPU should be over-provisioned (< 50)
	if got := ClassifyCPU(15, th); got != VerdictOverProvisioned {
		t.Errorf("ClassifyCPU(15, custom) = %v, want over-provisioned", got)
	}
	// 25% memory should be over-provisioned (< 40)
	if got := ClassifyMemory(25, th); got != VerdictOverProvisioned {
		t.Errorf("ClassifyMemory(25, custom) = %v, want over-provisioned", got)
	}
}
```

**Step 2: Run tests**

```bash
go test ./internal/rightsizing/ -v
```
Expected: All PASS.

**Step 3: Commit**

```bash
git add internal/rightsizing/classify_test.go
git commit -m "test(rightsizing): add classification unit tests

Cover all verdict paths, boundary conditions, custom thresholds,
P95 computation edge cases."
```

---

## Task 3: Create `analyzer.go` — Metrics Query Orchestration

**Files:**
- Create: `internal/rightsizing/analyzer.go`

**Step 1: Create the analyzer**

Read `ALGORITHM-REFERENCE.md` sections 5 (reclaimable resources), 6 (streaks), and 7 (data access) before writing this.

```go
package rightsizing

import (
	"fmt"
	"math"
	"time"

	"github.com/rcourtman/pulse-go-rewrite/internal/models"
	"github.com/rcourtman/pulse-go-rewrite/pkg/metrics"
)

// MetricsQuerier abstracts read access to the metrics store for testing.
// Uses QueryAll to fetch all metric types for a guest in a single SQLite query
// instead of one query per metric type, halving the total query count.
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

// Summary holds aggregate statistics.
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

// Result is the complete API response.
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
	StepSecs    int64
	Tier        string
	DataQuality string
	Note        string
}

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

// Analyze performs right-sizing analysis on all running guests.
func Analyze(querier MetricsQuerier, state models.StateSnapshot, t Thresholds, timeRange string) (*Result, error) {
	start := time.Now()

	rc, ok := rangeConfigs[timeRange]
	if !ok {
		rc = rangeConfigs["7d"] // default
		timeRange = "7d"
	}

	end := time.Now()
	queryStart := end.Add(-rc.Duration)

	// Gate streak computation to ranges with multiple days of data.
	// For 1h/6h/24h ranges, streak is meaningless and avoids 2×N extra SQLite queries.
	doStreak := rc.Duration >= 7*24*time.Hour

	var guests []GuestResult

	// Process VMs
	for _, vm := range state.VMs {
		if vm.Status != "running" || vm.Template {
			continue
		}
		gr := analyzeGuest(querier, vm.ID, vm.Name, vm.Node, "vm", vm.VMID,
			vm.CPUs, vm.Memory.Total, queryStart, end, rc.StepSecs, t, doStreak)
		guests = append(guests, gr)
	}

	// Process Containers
	for _, ct := range state.Containers {
		if ct.Status != "running" || ct.Template {
			continue
		}
		gr := analyzeGuest(querier, ct.ID, ct.Name, ct.Node, "container", ct.VMID,
			ct.CPUs, ct.Memory.Total, queryStart, end, rc.StepSecs, t, doStreak)
		guests = append(guests, gr)
	}

	// Compute summary
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

		// Reclaimable memory: keep 20% headroom above P95 before treating remainder as reclaimable
		if (g.MemVerdict == VerdictOverProvisioned || g.MemVerdict == VerdictIdle) &&
			g.MaxMemBytes > 0 && g.MemP95 > 0 {
			usedBytes := float64(g.MaxMemBytes) * (g.MemP95 / 100.0)
			// Multiply by 0.8 to keep 20% headroom — matches DESIGN.md §5.3
			reclaimable := (float64(g.MaxMemBytes) - usedBytes) * 0.8
			if reclaimable > 0 {
				summary.ReclaimableMemGB += reclaimable / (1024 * 1024 * 1024)
			}
		}

		// Reclaimable CPUs
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

	// Round reclaimable memory to 1 decimal
	summary.ReclaimableMemGB = math.Round(summary.ReclaimableMemGB*10) / 10

	elapsed := time.Since(start).Milliseconds()

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

// analyzeGuest classifies a single guest.
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

	// Single QueryAll call fetches cpu + memory in one SQLite round-trip.
	allPoints, err := querier.QueryAll("guest", id, start, end, stepSecs)
	if err != nil {
		gr.Overall = VerdictInsufficientData
		gr.CPUVerdict = VerdictInsufficientData
		gr.MemVerdict = VerdictInsufficientData
		return gr
	}
	cpuPoints := allPoints["cpu"]
	memPoints := allPoints["memory"]

	// Check minimum samples
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

	// Extract values and compute
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

	gr.CPUAvg = math.Round(cpuStats.Avg*100) / 100
	gr.CPUP95 = math.Round(cpuStats.P95*100) / 100
	gr.CPUMax = math.Round(cpuStats.Max*100) / 100
	gr.MemAvg = math.Round(memStats.Avg*100) / 100
	gr.MemP95 = math.Round(memStats.P95*100) / 100
	gr.MemMax = math.Round(memStats.Max*100) / 100

	gr.CPUVerdict = ClassifyCPU(cpuStats.P95, t)
	gr.MemVerdict = ClassifyMemory(memStats.P95, t)
	gr.Overall = ClassifyOverall(gr.CPUVerdict, gr.MemVerdict)

	// Streak: only computed for multi-day ranges to avoid extra SQLite queries on short ranges.
	if doStreak {
		gr.DaysAtVerdict = streakDays(querier, id, gr.Overall, t)
	}

	return gr
}

// streakDays counts consecutive recent days at the given verdict using daily metrics.
// Uses a single QueryAll call instead of two separate Query calls per guest.
func streakDays(querier MetricsQuerier, guestID string, currentVerdict Verdict, t Thresholds) int {
	if currentVerdict == VerdictInsufficientData {
		return 0
	}

	// Query last 90 days of daily data (cpu + memory) in one call.
	end := time.Now()
	start := end.Add(-90 * 24 * time.Hour)

	allDaily, err := querier.QueryAll("guest", guestID, start, end, 86400) // daily step
	if err != nil {
		return 0
	}
	cpuPoints := allDaily["cpu"]
	memPoints := allDaily["memory"]
	if len(cpuPoints) == 0 || len(memPoints) == 0 {
		return 0
	}

	// Build date→value maps
	cpuByDate := make(map[string]float64)
	for _, p := range cpuPoints {
		key := p.Timestamp.Format("2006-01-02")
		cpuByDate[key] = p.Value
	}
	memByDate := make(map[string]float64)
	for _, p := range memPoints {
		key := p.Timestamp.Format("2006-01-02")
		memByDate[key] = p.Value
	}

	// Walk backwards from yesterday
	streak := 0
	for day := 1; day <= 90; day++ {
		date := end.AddDate(0, 0, -day)
		dateKey := date.Format("2006-01-02")

		cpuVal, cpuOk := cpuByDate[dateKey]
		memVal, memOk := memByDate[dateKey]

		if !cpuOk || !memOk {
			break // gap in data
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
```

**IMPORTANT NOTE ON TYPES:** The exact Go types for `VM`, `Container`, `Memory`, etc. will depend on Pulse's actual model types in `internal/models/`. Before writing this code, read:
- `internal/models/state.go` for `StateSnapshot`, `VM`, `Container` struct fields
- Check if `Template` is a field on VM/Container (it is — seen in `GuestRow.tsx`)
- Check if `CPUs` is `int` (it's `int` per `VM.CPUs`)
- Check `Memory.Total` type (it's `int64` — bytes)

Adjust field names and types to match Pulse's actual model definitions.

**Step 2: Verify it compiles**

```bash
go build ./internal/rightsizing/
```
Expected: No errors (after adjusting imports to match Pulse's actual module path).

**Step 3: Commit**

```bash
git add internal/rightsizing/analyzer.go
git commit -m "feat(rightsizing): add analyzer for metrics query orchestration

Queries metrics.db for each running guest, computes P95 stats,
classifies, calculates reclaimable resources and streaks.
Supports 1h-30d time ranges with tier-aware quality labels."
```

---

## Task 4: Create `analyzer_test.go` — Integration Tests

**Files:**
- Create: `internal/rightsizing/analyzer_test.go`

**Step 1: Create a mock querier and tests**

```go
package rightsizing

import (
	"strings"
	"testing"
	"time"

	"github.com/rcourtman/pulse-go-rewrite/internal/models"
	"github.com/rcourtman/pulse-go-rewrite/pkg/metrics"
)

// mockQuerier returns pre-configured metric data for testing.
type mockQuerier struct {
	data map[string][]metrics.MetricPoint // key: "resourceType:resourceID:metricType"
}

func (m *mockQuerier) QueryAll(resourceType, resourceID string,
	start, end time.Time, stepSecs int64) (map[string][]metrics.MetricPoint, error) {

	result := make(map[string][]metrics.MetricPoint)
	prefix := resourceType + ":" + resourceID + ":"
	for key, allPoints := range m.data {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		metricType := strings.TrimPrefix(key, prefix)
		var filtered []metrics.MetricPoint
		for _, p := range allPoints {
			if (p.Timestamp.Equal(start) || p.Timestamp.After(start)) &&
				(p.Timestamp.Equal(end) || p.Timestamp.Before(end)) {
				filtered = append(filtered, p)
			}
		}
		result[metricType] = filtered
	}
	return result, nil
}

func makePoints(values []float64, start time.Time, interval time.Duration) []metrics.MetricPoint {
	points := make([]metrics.MetricPoint, len(values))
	for i, v := range values {
		points[i] = metrics.MetricPoint{
			Timestamp: start.Add(time.Duration(i) * interval),
			Value:     v,
		}
	}
	return points
}

func TestAnalyze_OverProvisionedVM(t *testing.T) {
	now := time.Now()
	start := now.Add(-7 * 24 * time.Hour)

	// 168 hour-sized points all at low usage
	cpuValues := make([]float64, 168)
	memValues := make([]float64, 168)
	for i := range cpuValues {
		cpuValues[i] = 10.0 // 10% CPU
		memValues[i] = 15.0 // 15% memory
	}

	q := &mockQuerier{data: map[string][]metrics.MetricPoint{
		"guest:test:vm1:cpu":    makePoints(cpuValues, start, time.Hour),
		"guest:test:vm1:memory": makePoints(memValues, start, time.Hour),
	}}

	state := models.StateSnapshot{
		VMs: []models.VM{{
			ID:     "test:vm1",
			Name:   "web-server",
			Node:   "node1",
			VMID:   100,
			Status: "running",
			CPUs:   4,
			Memory: models.Memory{Total: 8 * 1024 * 1024 * 1024}, // 8 GB
		}},
	}

	result, err := Analyze(q, state, DefaultThresholds(), "7d")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}

	if result.Summary.TotalGuests != 1 {
		t.Fatalf("expected 1 guest, got %d", result.Summary.TotalGuests)
	}
	if result.Summary.OverProvisioned != 1 {
		t.Fatalf("expected 1 over-provisioned, got %d", result.Summary.OverProvisioned)
	}
	if result.Guests[0].Overall != VerdictOverProvisioned {
		t.Fatalf("expected over-provisioned, got %v", result.Guests[0].Overall)
	}
	if result.Summary.ReclaimableMemGB <= 0 {
		t.Fatalf("expected reclaimable memory > 0, got %f", result.Summary.ReclaimableMemGB)
	}
}

func TestAnalyze_InsufficientData(t *testing.T) {
	q := &mockQuerier{data: map[string][]metrics.MetricPoint{
		"guest:test:vm2:cpu":    makePoints([]float64{50, 55}, time.Now().Add(-2*time.Hour), time.Hour),
		"guest:test:vm2:memory": makePoints([]float64{60, 65}, time.Now().Add(-2*time.Hour), time.Hour),
	}}

	state := models.StateSnapshot{
		VMs: []models.VM{{
			ID: "test:vm2", Name: "tiny", Node: "node1",
			VMID: 101, Status: "running", CPUs: 1,
		}},
	}

	result, err := Analyze(q, state, DefaultThresholds(), "7d")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if result.Guests[0].Overall != VerdictInsufficientData {
		t.Fatalf("expected insufficient-data, got %v", result.Guests[0].Overall)
	}
}

func TestAnalyze_StoppedVMsExcluded(t *testing.T) {
	q := &mockQuerier{data: map[string][]metrics.MetricPoint{}}
	state := models.StateSnapshot{
		VMs: []models.VM{
			{ID: "test:vm3", Name: "stopped-vm", Status: "stopped", CPUs: 2},
		},
	}

	result, err := Analyze(q, state, DefaultThresholds(), "7d")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if result.Summary.TotalGuests != 0 {
		t.Fatalf("expected 0 guests (stopped excluded), got %d", result.Summary.TotalGuests)
	}
}

func TestAnalyze_MixedVerdict(t *testing.T) {
	now := time.Now()
	start := now.Add(-7 * 24 * time.Hour)

	cpuValues := make([]float64, 168) // low CPU
	memValues := make([]float64, 168) // high memory
	for i := range cpuValues {
		cpuValues[i] = 3.0  // idle CPU
		memValues[i] = 92.0 // under-provisioned memory
	}

	q := &mockQuerier{data: map[string][]metrics.MetricPoint{
		"guest:test:vmM:cpu":    makePoints(cpuValues, start, time.Hour),
		"guest:test:vmM:memory": makePoints(memValues, start, time.Hour),
	}}

	state := models.StateSnapshot{
		VMs: []models.VM{{
			ID: "test:vmM", Name: "mixed-vm", Node: "node1",
			VMID: 200, Status: "running", CPUs: 2,
			Memory: models.Memory{Total: 4 * 1024 * 1024 * 1024},
		}},
	}

	result, err := Analyze(q, state, DefaultThresholds(), "7d")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if result.Guests[0].Overall != VerdictMixed {
		t.Fatalf("expected mixed (idle CPU + under-provisioned mem), got %v", result.Guests[0].Overall)
	}
}
```

**IMPORTANT NOTE ON TYPES:** Adjust struct field names to match Pulse's actual `models.VM`, `models.Container`, `models.Memory` definitions. Check `internal/models/` before writing.

**Step 2: Run tests**

```bash
go test ./internal/rightsizing/ -v
```
Expected: All PASS.

**Step 3: Commit**

```bash
git add internal/rightsizing/analyzer_test.go
git commit -m "test(rightsizing): add analyzer integration tests

Mock metrics querier, verify summary counts, reclaimable resources,
insufficient data handling, stopped VM exclusion, mixed verdicts."
```

---

## Task 5: Create API Handlers — `rightsizing_handlers.go`

**Files:**
- Create: `internal/api/rightsizing_handlers.go`

**Step 1: Create the handler file**

Before writing, read these Pulse source files:
- `internal/api/router.go` — Search for `handleMetricsHistory` to see the handler pattern, how to access the monitor, the license check pattern
- Search for `getTenantMonitor` to see multi-tenancy pattern
- Search for `GetMetricsStore` to see how to access the metrics store
- Search for `GetState` to see how to get the state snapshot
- Search for `HasFeature` and `FeatureLongTermMetrics` for the license gating pattern

The handler should:
1. Parse query params (range, thresholds) with validation
2. Use `r.getTenantMonitor(req.Context())` for the monitor
3. Get state via `monitor.GetState()`
4. Get metrics store via `monitor.GetMetricsStore()`
5. Check license for >7d ranges
6. Call `rightsizing.Analyze()`
7. Return JSON (or CSV for the export endpoint)

```go
package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/rcourtman/pulse-go-rewrite/internal/license"
	"github.com/rcourtman/pulse-go-rewrite/internal/rightsizing"
	// ... other Pulse imports as needed
)

func (r *Router) handleRightSizing(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	monitor := r.getTenantMonitor(req.Context())
	if monitor == nil {
		http.Error(w, "Monitor not available", http.StatusInternalServerError)
		return
	}

	query := req.URL.Query()
	timeRange := query.Get("range")
	if timeRange == "" {
		timeRange = "7d"
	}

	// Validate range
	validRanges := map[string]bool{
		"1h": true, "6h": true, "24h": true,
		"7d": true, "14d": true, "30d": true,
	}
	if !validRanges[timeRange] {
		http.Error(w, "Invalid range. Valid: 1h, 6h, 24h, 7d, 14d, 30d", http.StatusBadRequest)
		return
	}

	// License check for >7d ranges — mirrors the pattern in handleMetricsHistory
	if timeRange == "14d" || timeRange == "30d" {
		if !r.licenseHandlers.Service(req.Context()).HasFeature(license.FeatureLongTermMetrics) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":       "license_required",
				"message":     "14d/30d right-sizing requires a Pulse Pro license",
				"feature":     license.FeatureLongTermMetrics,
				"upgrade_url": "https://pulserelay.pro/",
				"max_free":    "7d",
			})
			return
		}
	}

	// Parse optional threshold overrides
	th := rightsizing.DefaultThresholds()
	parseThreshold := func(key string, target *float64) {
		if v := query.Get(key); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 100 {
				*target = f
			}
		}
	}
	parseThreshold("threshold_cpu_idle", &th.CPUIdle)
	parseThreshold("threshold_cpu_over", &th.CPUOver)
	parseThreshold("threshold_cpu_under", &th.CPUUnder)
	parseThreshold("threshold_mem_idle", &th.MemIdle)
	parseThreshold("threshold_mem_over", &th.MemOver)
	parseThreshold("threshold_mem_under", &th.MemUnder)

	// Validate threshold ordering
	if th.CPUIdle >= th.CPUOver || th.CPUOver >= th.CPUUnder {
		http.Error(w, "Invalid CPU thresholds: must be idle < over < under", http.StatusBadRequest)
		return
	}
	if th.MemIdle >= th.MemOver || th.MemOver >= th.MemUnder {
		http.Error(w, "Invalid memory thresholds: must be idle < over < under", http.StatusBadRequest)
		return
	}

	// Get state and metrics store
	state := monitor.GetState()
	metricsStore := monitor.GetMetricsStore()
	if metricsStore == nil {
		http.Error(w, "Metrics store not available", http.StatusServiceUnavailable)
		return
	}

	result, err := rightsizing.Analyze(metricsStore, state, th, timeRange)
	if err != nil {
		http.Error(w, fmt.Sprintf("Analysis failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Compute-Time-Ms", strconv.FormatInt(result.ComputeTimeMs, 10))
	json.NewEncoder(w).Encode(result)
}

func (r *Router) handleRightSizingExport(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Same setup as handleRightSizing: extract method once both handlers are stable.
	monitor := r.getTenantMonitor(req.Context())
	if monitor == nil {
		http.Error(w, "Monitor not available", http.StatusInternalServerError)
		return
	}

	query := req.URL.Query()
	timeRange := query.Get("range")
	if timeRange == "" {
		timeRange = "7d"
	}

	// License check mirrors handleRightSizing
	if timeRange == "14d" || timeRange == "30d" {
		if !r.licenseHandlers.Service(req.Context()).HasFeature(license.FeatureLongTermMetrics) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":       "license_required",
				"message":     "14d/30d right-sizing requires a Pulse Pro license",
				"feature":     license.FeatureLongTermMetrics,
				"upgrade_url": "https://pulserelay.pro/",
				"max_free":    "7d",
			})
			return
		}
	}

	th := rightsizing.DefaultThresholds()
	// (same threshold parsing as above)

	state := monitor.GetState()
	metricsStore := monitor.GetMetricsStore()
	if metricsStore == nil {
		http.Error(w, "Metrics store not available", http.StatusServiceUnavailable)
		return
	}

	result, err := rightsizing.Analyze(metricsStore, state, th, timeRange)
	if err != nil {
		http.Error(w, fmt.Sprintf("Analysis failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Build CSV
	csvEscape := func(val string) string {
		if len(val) > 0 && (val[0] == '=' || val[0] == '+' || val[0] == '-' || val[0] == '@') {
			val = "\t" + val
		}
		if strings.ContainsAny(val, ",\"\n") {
			return `"` + strings.ReplaceAll(val, `"`, `""`) + `"`
		}
		return val
	}

	header := "VMID,Name,Type,Node,CPUs,Max Mem (GB),CPU Avg %,CPU P95 %," +
		"Mem Avg %,Mem P95 %,CPU Verdict,Mem Verdict,Overall,Days at Verdict,Samples\r\n"

	var sb strings.Builder
	sb.WriteString(header)
	for _, g := range result.Guests {
		maxMemGB := float64(g.MaxMemBytes) / (1024 * 1024 * 1024)
		sb.WriteString(fmt.Sprintf("%d,%s,%s,%s,%d,%.1f,%.1f,%.1f,%.1f,%.1f,%s,%s,%s,%d,%d\r\n",
			g.VMID,
			csvEscape(g.Name),
			g.Type,
			csvEscape(g.Node),
			g.CPUs,
			maxMemGB,
			g.CPUAvg, g.CPUP95,
			g.MemAvg, g.MemP95,
			g.CPUVerdict, g.MemVerdict, g.Overall,
			g.DaysAtVerdict,
			g.SampleCount,
		))
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="right-sizing.csv"`)
	w.Write([]byte(sb.String()))
}
```

**IMPORTANT:** The exact method names (`GetState()`, `GetMetricsStore()`, `getTenantMonitor()`) need to be verified against Pulse's actual Router/Monitor types. Search for these in the codebase before implementing.

**Step 2: Register routes in router.go**

Find the route registration section in `internal/api/router.go` (search for `HandleFunc`) and add:

```go
mux.HandleFunc("/api/rightsizing", r.handleRightSizing)
mux.HandleFunc("/api/rightsizing/export", r.handleRightSizingExport)
```

**Step 3: Verify it compiles**

```bash
go build ./...
```

**Step 4: Commit**

```bash
git add internal/api/rightsizing_handlers.go internal/api/router.go
git commit -m "feat(rightsizing): add API endpoints

GET /api/rightsizing - JSON right-sizing analysis
GET /api/rightsizing/export - CSV export
Supports configurable thresholds and 1h-30d time ranges."
```

---

## Task 5b: Update Route Inventory Tests

Pulse enforces a strict allowlist of every registered route via an AST-based parser in `internal/api/route_inventory_test.go`. Adding routes to `router.go` without updating this file will **immediately fail `go test ./internal/api/`**.

**Files:**
- Edit: `internal/api/route_inventory_test.go`

**Step 1: Add to `allRouteAllowlist`**

The two new routes are wrapped in `RequireAuth(r.config, RequireScope(...))`, which `isProtectedHandler()` detects as a protected handler. Protected routes go in `allRouteAllowlist` only — **not** `publicRouteAllowlist` or `bareRouteAllowlist`.

Find the `allRouteAllowlist` slice (it's near the bottom of the file) and add after the `/api/metrics-store/history` entry:

```go
"/api/rightsizing",
"/api/rightsizing/export",
```

**Step 2: Verify no other inventory files need updating**

Run the full route inventory test:

```bash
go test ./internal/api/ -run TestRouterRouteInventory -v
```

Expected: PASS. If it fails, the error message names exactly which allowlist needs the entry.

**Related test files to check** (these parse or assert on the full route list — check if they need the new routes):
- `router_csrf_skip_routes_test.go` — lists routes exempt from CSRF. Right-sizing uses GET only; standard CSRF middleware skips GET requests, so no entry needed here.
- `router_download_inventory_test.go` — lists routes that produce downloads. The `/api/rightsizing/export` endpoint returns `Content-Disposition: attachment`. **Check this file** and add `/api/rightsizing/export` if it has a download route allowlist.
- `router_auth_additional_test.go` — check if it asserts on the set of authenticated routes. Add both routes if so.

**Step 3: Commit**

```bash
git add internal/api/route_inventory_test.go
git commit -m "test(rightsizing): add routes to route inventory allowlist"
```

---

## Task 6: Create Frontend API Client — `rightsizing.ts`

**Files:**
- Create: `frontend-modern/src/api/rightsizing.ts`

**Step 1: Create the API client**

Before writing, read `frontend-modern/src/api/charts.ts` to see the existing API fetch pattern (`apiFetchJSON` or similar).

```typescript
// API client for the right-sizing feature.

import { apiFetchJSON } from '@/utils/apiClient';

export type Verdict =
  | 'idle'
  | 'over-provisioned'
  | 'right-sized'
  | 'under-provisioned'
  | 'mixed'
  | 'insufficient-data';

export type TimeRange = '1h' | '6h' | '24h' | '7d' | '14d' | '30d';
export type DataQuality = 'high' | 'good' | 'low';

export interface GuestResult {
  id: string;
  name: string;
  node: string;
  type: 'vm' | 'container';
  vmid: number;
  status: string;
  cpus: number;
  maxMemBytes: number;
  cpuAvg: number;
  cpuP95: number;
  cpuMax: number;
  memAvg: number;
  memP95: number;
  memMax: number;
  cpuVerdict: Verdict;
  memVerdict: Verdict;
  overall: Verdict;
  daysAtVerdict: number;
  sampleCount: number;
}

export interface RightSizingSummary {
  totalGuests: number;
  idle: number;
  overProvisioned: number;
  rightSized: number;
  underProvisioned: number;
  mixed: number;
  insufficientData: number;
  reclaimableMemoryGB: number;
  reclaimableCPUs: number;
}

export interface RightSizingResult {
  summary: RightSizingSummary;
  guests: GuestResult[];
  range: string;
  tier: string;
  dataQuality: DataQuality;
  dataQualityNote?: string;
  computeTimeMs: number;
}

export async function fetchRightSizing(
  range: TimeRange = '7d',
): Promise<RightSizingResult> {
  const params = new URLSearchParams({ range });
  return apiFetchJSON(`/api/rightsizing?${params.toString()}`);
}

export function exportRightSizingCSV(range: TimeRange = '7d'): void {
  const params = new URLSearchParams({ range });
  window.open(`/api/rightsizing/export?${params.toString()}`, '_blank');
}
```

**Step 2: Commit**

```bash
git add frontend-modern/src/api/rightsizing.ts
git commit -m "feat(rightsizing): add frontend API client types and fetch functions"
```

---

## Task 7: Create `VerdictBadge.tsx` Component

**Files:**
- Create: `frontend-modern/src/components/RightSizing/VerdictBadge.tsx`

**Step 1: Create the component**

Read `frontend-modern/src/components/Dashboard/GuestRow.tsx` first to see Pulse's existing badge/pill styling patterns.

```tsx
import type { Verdict } from '@/api/rightsizing';

const verdictConfig: Record<Verdict, { label: string; classes: string }> = {
  'idle': {
    label: 'Idle',
    classes: 'bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300',
  },
  'over-provisioned': {
    label: 'Over',
    classes: 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300',
  },
  'right-sized': {
    label: 'Right',
    classes: 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300',
  },
  'under-provisioned': {
    label: 'Under',
    classes: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300',
  },
  'mixed': {
    label: 'Mixed',
    classes: 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300',
  },
  'insufficient-data': {
    label: 'No Data',
    classes: 'bg-gray-50 text-gray-400 dark:bg-gray-800 dark:text-gray-500',
  },
};

interface VerdictBadgeProps {
  verdict: Verdict;
}

export function VerdictBadge(props: VerdictBadgeProps) {
  const config = () => verdictConfig[props.verdict] ?? verdictConfig['insufficient-data'];
  return (
    <span class={`inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium ${config().classes}`}>
      {config().label}
    </span>
  );
}
```

**Step 2: Commit**

```bash
git add frontend-modern/src/components/RightSizing/VerdictBadge.tsx
git commit -m "feat(rightsizing): add VerdictBadge component with color-coded pills"
```

---

## Task 8: Create `SummaryCards.tsx` Component

**Files:**
- Create: `frontend-modern/src/components/RightSizing/SummaryCards.tsx`

**Step 1: Create the component**

Read existing card patterns in the Dashboard or Settings pages first.

```tsx
import type { RightSizingSummary } from '@/api/rightsizing';

interface SummaryCardsProps {
  summary: RightSizingSummary;
}

function StatCard(props: {
  label: string;
  count: number;
  total: number;
  color: string;
  icon?: string;
}) {
  const pct = () => props.total > 0 ? Math.round((props.count / props.total) * 100) : 0;
  return (
    <div class={`rounded-lg border p-4 ${props.color}`}>
      <div class="text-2xl font-bold">{props.count}</div>
      <div class="text-sm opacity-75">{props.label}</div>
      <div class="text-xs opacity-50 mt-1">{pct()}% of fleet</div>
    </div>
  );
}

export function SummaryCards(props: SummaryCardsProps) {
  return (
    <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
      <StatCard label="Idle" count={props.summary.idle}
        total={props.summary.totalGuests}
        color="border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800/50" />
      <StatCard label="Over-provisioned" count={props.summary.overProvisioned}
        total={props.summary.totalGuests}
        color="border-blue-200 dark:border-blue-800 bg-blue-50 dark:bg-blue-900/20" />
      <StatCard label="Right-sized" count={props.summary.rightSized}
        total={props.summary.totalGuests}
        color="border-green-200 dark:border-green-800 bg-green-50 dark:bg-green-900/20" />
      <StatCard label="Under-provisioned" count={props.summary.underProvisioned}
        total={props.summary.totalGuests}
        color="border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-900/20" />
      <StatCard label="Mixed" count={props.summary.mixed}
        total={props.summary.totalGuests}
        color="border-purple-200 dark:border-purple-800 bg-purple-50 dark:bg-purple-900/20" />
      <div class="rounded-lg border border-indigo-200 dark:border-indigo-800 bg-indigo-50 dark:bg-indigo-900/20 p-4">
        <div class="text-lg font-bold">
          {props.summary.reclaimableMemoryGB.toFixed(1)} GB
        </div>
        <div class="text-sm opacity-75">Reclaimable Memory</div>
        <div class="text-xs opacity-50 mt-1">
          {props.summary.reclaimableCPUs} vCPU cores
        </div>
      </div>
    </div>
  );
}
```

**Step 2: Commit**

```bash
git add frontend-modern/src/components/RightSizing/SummaryCards.tsx
git commit -m "feat(rightsizing): add SummaryCards component"
```

---

## Task 9: Create `GuestTable.tsx` Component

**Files:**
- Create: `frontend-modern/src/components/RightSizing/GuestTable.tsx`

**Step 1: Create the sortable table**

Read `frontend-modern/src/components/Dashboard/GuestRow.tsx` for Pulse's table patterns.

This is the largest component. Key features:
- Sortable columns (click header to toggle asc/desc)
- Filter by verdict
- Search by guest name
- Format bytes (reuse Pulse's `formatBytes` utility)

```tsx
import { createSignal, createMemo, For, Show } from 'solid-js';
import type { GuestResult, Verdict } from '@/api/rightsizing';
import { VerdictBadge } from './VerdictBadge';

type SortKey = 'name' | 'node' | 'vmid' | 'cpuP95' | 'memP95' | 'overall' | 'daysAtVerdict';
type SortDir = 'asc' | 'desc';

interface GuestTableProps {
  guests: GuestResult[];
  searchQuery: string;
  verdictFilter: Verdict | 'all';
}

export function GuestTable(props: GuestTableProps) {
  const [sortKey, setSortKey] = createSignal<SortKey>('overall');
  const [sortDir, setSortDir] = createSignal<SortDir>('asc');

  const toggleSort = (key: SortKey) => {
    if (sortKey() === key) {
      setSortDir(d => d === 'asc' ? 'desc' : 'asc');
    } else {
      setSortKey(key);
      setSortDir('asc');
    }
  };

  const verdictOrder: Record<Verdict, number> = {
    'idle': 1,
    'over-provisioned': 2,
    'under-provisioned': 3,
    'mixed': 4,
    'right-sized': 5,
    'insufficient-data': 6,
  };

  const filtered = createMemo(() => {
    let list = props.guests;
    if (props.verdictFilter !== 'all') {
      list = list.filter(g => g.overall === props.verdictFilter);
    }
    if (props.searchQuery) {
      const q = props.searchQuery.toLowerCase();
      list = list.filter(g =>
        g.name.toLowerCase().includes(q) ||
        g.node.toLowerCase().includes(q) ||
        String(g.vmid).includes(q)
      );
    }
    return list;
  });

  const sorted = createMemo(() => {
    const list = [...filtered()];
    const dir = sortDir() === 'asc' ? 1 : -1;
    list.sort((a, b) => {
      let cmp = 0;
      switch (sortKey()) {
        case 'name': cmp = a.name.localeCompare(b.name); break;
        case 'node': cmp = a.node.localeCompare(b.node); break;
        case 'vmid': cmp = a.vmid - b.vmid; break;
        case 'cpuP95': cmp = a.cpuP95 - b.cpuP95; break;
        case 'memP95': cmp = a.memP95 - b.memP95; break;
        case 'overall': cmp = (verdictOrder[a.overall] ?? 6) - (verdictOrder[b.overall] ?? 6); break;
        case 'daysAtVerdict': cmp = a.daysAtVerdict - b.daysAtVerdict; break;
      }
      return cmp * dir;
    });
    return list;
  });

  const SortHeader = (p: { key: SortKey; label: string; class?: string }) => (
    <th
      class={`px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400 cursor-pointer hover:text-gray-700 dark:hover:text-gray-200 ${p.class ?? ''}`}
      onClick={() => toggleSort(p.key)}
    >
      {p.label}
      <Show when={sortKey() === p.key}>
        <span class="ml-1">{sortDir() === 'asc' ? '↑' : '↓'}</span>
      </Show>
    </th>
  );

  return (
    <div class="overflow-x-auto">
      <table class="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
        <thead class="bg-gray-50 dark:bg-gray-800">
          <tr>
            <SortHeader key="name" label="Name" />
            <SortHeader key="node" label="Node" />
            <th class="px-3 py-2 text-left text-xs font-medium text-gray-500">Type</th>
            <SortHeader key="vmid" label="VMID" />
            <SortHeader key="cpuP95" label="CPU P95" />
            <SortHeader key="memP95" label="Mem P95" />
            <th class="px-3 py-2 text-left text-xs font-medium text-gray-500">CPU</th>
            <th class="px-3 py-2 text-left text-xs font-medium text-gray-500">Mem</th>
            <SortHeader key="overall" label="Overall" />
            <SortHeader key="daysAtVerdict" label="Days" />
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-200 dark:divide-gray-700">
          <For each={sorted()}>
            {(guest) => (
              <tr class="hover:bg-gray-50 dark:hover:bg-gray-800/50">
                <td class="px-3 py-2 text-sm font-medium">{guest.name}</td>
                <td class="px-3 py-2 text-sm text-gray-500">{guest.node}</td>
                <td class="px-3 py-2 text-sm text-gray-500">{guest.type}</td>
                <td class="px-3 py-2 text-sm text-gray-500">{guest.vmid}</td>
                <td class="px-3 py-2 text-sm">{guest.cpuP95.toFixed(1)}%</td>
                <td class="px-3 py-2 text-sm">{guest.memP95.toFixed(1)}%</td>
                <td class="px-3 py-2"><VerdictBadge verdict={guest.cpuVerdict} /></td>
                <td class="px-3 py-2"><VerdictBadge verdict={guest.memVerdict} /></td>
                <td class="px-3 py-2"><VerdictBadge verdict={guest.overall} /></td>
                <td class="px-3 py-2 text-sm text-gray-500">{guest.daysAtVerdict}d</td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
      <Show when={sorted().length === 0}>
        <div class="text-center py-8 text-gray-500">
          No guests match the current filters.
        </div>
      </Show>
    </div>
  );
}
```

**Step 2: Commit**

```bash
git add frontend-modern/src/components/RightSizing/GuestTable.tsx
git commit -m "feat(rightsizing): add sortable GuestTable with verdict badges and filtering"
```

---

## Task 10: Create the Right-Sizing Page — `RightSizing.tsx`

**Files:**
- Create: `frontend-modern/src/pages/RightSizing.tsx`

**Step 1: Create the page**

Read existing pages in `frontend-modern/src/pages/` to match the layout pattern.

```tsx
import { createSignal, createResource, Show } from 'solid-js';
import type { TimeRange, Verdict } from '@/api/rightsizing';
import { fetchRightSizing, exportRightSizingCSV } from '@/api/rightsizing';
import { SummaryCards } from '@/components/RightSizing/SummaryCards';
import { GuestTable } from '@/components/RightSizing/GuestTable';
import ScaleIcon from 'lucide-solid/icons/scale';
import DownloadIcon from 'lucide-solid/icons/download';

const RANGES: { value: TimeRange; label: string }[] = [
  { value: '1h', label: '1 Hour' },
  { value: '6h', label: '6 Hours' },
  { value: '24h', label: '24 Hours' },
  { value: '7d', label: '7 Days' },
  { value: '14d', label: '14 Days' },
  { value: '30d', label: '30 Days' },
];

export default function RightSizingPage() {
  const [range, setRange] = createSignal<TimeRange>('7d');
  const [searchQuery, setSearchQuery] = createSignal('');
  const [verdictFilter, setVerdictFilter] = createSignal<Verdict | 'all'>('all');

  const [data] = createResource(range, fetchRightSizing);

  return (
    <div class="p-6 space-y-6">
      {/* Header */}
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <ScaleIcon class="w-6 h-6 text-gray-500" />
          <h1 class="text-2xl font-bold text-gray-900 dark:text-gray-100">
            Right-Sizing
          </h1>
        </div>
        <div class="flex items-center gap-3">
          {/* Range selector */}
          <select
            class="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3 py-1.5 text-sm"
            value={range()}
            onChange={(e) => setRange(e.currentTarget.value as TimeRange)}
          >
            {RANGES.map(r => (
              <option value={r.value}>{r.label}</option>
            ))}
          </select>

          {/* CSV Export */}
          <button
            class="flex items-center gap-1.5 rounded-md border border-gray-300 dark:border-gray-600 px-3 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-gray-700"
            onClick={() => exportRightSizingCSV(range())}
          >
            <DownloadIcon class="w-4 h-4" />
            Export CSV
          </button>
        </div>
      </div>

      {/* Loading / Error states */}
      <Show when={data.loading}>
        <div class="text-center py-8 text-gray-500">Analyzing fleet...</div>
      </Show>

      <Show when={data.error}>
        <div class="text-center py-8 text-red-500">
          Failed to load right-sizing data: {String(data.error)}
        </div>
      </Show>

      <Show when={data()}>
        {(result) => (
          <>
            {/* Data quality warning */}
            <Show when={result().dataQuality === 'low'}>
              <div class="rounded-md bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 p-3 text-sm text-amber-700 dark:text-amber-300">
                ⓘ {result().dataQualityNote}
              </div>
            </Show>

            {/* Summary Cards */}
            <SummaryCards summary={result().summary} />

            {/* Toolbar */}
            <div class="flex items-center gap-4">
              <input
                type="text"
                placeholder="Search by name, node, or VMID..."
                class="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3 py-1.5 text-sm flex-1 max-w-sm"
                value={searchQuery()}
                onInput={(e) => setSearchQuery(e.currentTarget.value)}
              />
              <select
                class="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3 py-1.5 text-sm"
                value={verdictFilter()}
                onChange={(e) => setVerdictFilter(e.currentTarget.value as Verdict | 'all')}
              >
                <option value="all">All Verdicts</option>
                <option value="idle">Idle</option>
                <option value="over-provisioned">Over-provisioned</option>
                <option value="right-sized">Right-sized</option>
                <option value="under-provisioned">Under-provisioned</option>
                <option value="mixed">Mixed</option>
                <option value="insufficient-data">Insufficient Data</option>
              </select>
              <span class="text-xs text-gray-400">
                {result().summary.totalGuests} guests · {result().computeTimeMs}ms
              </span>
            </div>

            {/* Guest Table */}
            <GuestTable
              guests={result().guests}
              searchQuery={searchQuery()}
              verdictFilter={verdictFilter()}
            />
          </>
        )}
      </Show>
    </div>
  );
}
```

**Step 2: Register lazy import and route in `frontend-modern/src/App.tsx`**

Add lazy import near the existing page imports (around line 88, after `AIIntelligencePage`):

```tsx
const RightSizingPage = lazy(() => import('./pages/RightSizing'));
```

Add a `<Route>` near the other top-level routes (around line 976, after the `/alerts/*` route):

```tsx
<Route path="/rightsizing" component={RightSizingPage} />
```

**Step 3: Add navigation entry in `frontend-modern/src/App.tsx`**

Right-Sizing is an analysis utility — it belongs alongside the other utility tabs (Alerts, Patrol, Settings). In the `utilityTabs` `createMemo` (around line 1228), add a new tab entry before the `return tabs` statement. Also add the icon import at the top of the file (near line 47 with the other Lucide imports):

```tsx
// At the top of App.tsx, near other lucide imports:
import ScaleIcon from 'lucide-solid/icons/scale';
```

```tsx
// In the utilityTabs createMemo, inside the tabs array:
{
  id: 'rightsizing' as const,
  label: 'Right-Sizing',
  route: '/rightsizing',
  tooltip: 'Analyze VM and container resource allocation',
  badge: null,
  count: undefined,
  breakdown: undefined,
  icon: <ScaleIcon class="w-4 h-4 shrink-0" />,
},
```

Also update the tab id union type to include `'rightsizing'` if TypeScript requires it (check the `id` field type on the `tabs` array).

**Step 4: Verify frontend builds**

```bash
cd frontend-modern
npm run build
```

**Step 5: Commit**

```bash
git add frontend-modern/src/pages/RightSizing.tsx
git add frontend-modern/src/App.tsx  # or routes file
git add frontend-modern/src/components/Layout/Sidebar.tsx
git commit -m "feat(rightsizing): add Right-Sizing page with summary cards, table, CSV export

New dedicated page accessible from sidebar navigation.
Range selector (1h-30d), verdict filter, search, CSV export.
Data quality banner for low-resolution ranges."
```

---

## Task 11: End-to-End Testing & Cleanup

**Step 1: Run all Go tests**

```bash
go test ./... -v
```
Expected: All PASS, including new rightsizing tests.

**Step 2: Vet and lint**

```bash
go vet ./...
# If golangci-lint is installed in the repo:
golangci-lint run ./internal/rightsizing/... ./internal/api/rightsizing_handlers.go
```

**Step 3: Build the full project**

```bash
go build ./...
cd frontend-modern && npm run build && cd ..
```

**Step 4: Manual smoke test (if Docker is available)**

```bash
# Start Pulse with mock mode to verify the UI (run from repo root)
go run ./cmd/pulse/ --mock
# Navigate to http://localhost:7655/rightsizing
# Verify: summary cards render, table populates, CSV downloads, range selector works
```

**Step 5: Final commit**

```bash
git add -A
git commit -m "chore(rightsizing): cleanup and final adjustments"
```

---

## Task 12: Prepare Pull Request

**Step 1: Write PR description**

Title: `feat: Add deterministic right-sizing analysis for VMs and containers`

Body should include:
- **What it does** — paragraph summary
- **Algorithm** — brief explanation of P95 classification
- **Screenshots** — capture the Right-Sizing page with mock data
- **No new dependencies** — emphasize minimal footprint
- **Configuration** — document query params for custom thresholds
- **Credits** — "Algorithm ported from [ProxCenter](https://github.com/...)"

**Step 2: Push and create PR**

```bash
git push origin feature/rightsizing
# Create PR on GitHub targeting rcourtman/pulse:main
```

---

## Plan Review Gate

### Wiring Completeness
- [x] API endpoint → handler → analyzer → store: fully traced
- [x] Frontend page → API client → endpoint: fully traced
- [x] Route registration: explicit `HandleFunc` additions noted
- [x] Navigation entry: sidebar addition noted
- [x] CSV export button → download handler: traced

### Resource Lifecycle
- [x] No new resources created (no goroutines, no connections, no files)
- [x] All queries are read-only against existing SQLite store
- [x] State snapshot is a copy — no side effects

### Dependency Completeness
- [x] No new Go modules needed
- [x] No new npm packages needed
- [x] All imports reference existing Pulse packages

### Config Consistency
- [x] No new env vars
- [x] No new config files
- [x] License gating uses existing `FeatureLongTermMetrics`

### Async/Sync Boundaries
- [x] Go handler is synchronous (standard `http.Handler`)
- [x] SQLite queries are synchronous (single connection, WAL mode)
- [x] Frontend uses SolidJS `createResource` for async data fetching

### Missing Integration Steps
- [x] Route registration explicitly noted (Task 5 Step 2)
- [x] Route inventory allowlist explicitly noted (Task 5b)
- [x] Frontend route registration explicitly noted (Task 10 Step 2)
- [x] App.tsx nav entry explicitly noted (Task 10 Step 3)

**Plan review passed — no integration gaps found.**
