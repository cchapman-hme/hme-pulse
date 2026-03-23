// Package rightsizing provides deterministic, P95-based VM and container
// right-sizing analysis. It classifies CPU and memory utilization into
// verdicts (idle, over-provisioned, right-sized, under-provisioned, mixed)
// and aggregates results across a fleet for actionable capacity reporting.
//
// Design: all functions are pure (no I/O, no goroutines). The caller is
// responsible for fetching metric data and enforcing Thresholds.MinSamples
// before invoking classification functions.
package rightsizing

import (
	"fmt"
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
// Precondition: CPUIdle < CPUOver < CPUUnder and MemIdle < MemOver < MemUnder must hold.
// Use DefaultThresholds() for sane defaults. Validate with ValidateThresholds() if
// accepting user-supplied values.
type Thresholds struct {
	CPUIdle    float64 // P95 CPU below this → idle (default: 5)
	CPUOver    float64 // P95 CPU below this → over-provisioned (default: 30)
	CPUUnder   float64 // P95 CPU above this → under-provisioned (default: 85)
	MemIdle    float64 // P95 Mem below this → idle (default: 10)
	MemOver    float64 // P95 Mem below this → over-provisioned (default: 30)
	MemUnder   float64 // P95 Mem above this → under-provisioned (default: 90)
	MinSamples int // Minimum data points for a valid classification. Callers MUST check
	               // len(points) >= MinSamples and return VerdictInsufficientData if not met.
	               // ClassifyCPU and ClassifyMemory do not enforce this — they classify any input.
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

// ValidateThresholds returns an error if the threshold ordering invariant is violated.
// CPU and memory thresholds must each satisfy: idle < over < under.
func ValidateThresholds(t Thresholds) error {
	if t.CPUIdle >= t.CPUOver {
		return fmt.Errorf("rightsizing: CPUIdle (%.1f) must be less than CPUOver (%.1f)", t.CPUIdle, t.CPUOver)
	}
	if t.CPUOver >= t.CPUUnder {
		return fmt.Errorf("rightsizing: CPUOver (%.1f) must be less than CPUUnder (%.1f)", t.CPUOver, t.CPUUnder)
	}
	if t.MemIdle >= t.MemOver {
		return fmt.Errorf("rightsizing: MemIdle (%.1f) must be less than MemOver (%.1f)", t.MemIdle, t.MemOver)
	}
	if t.MemOver >= t.MemUnder {
		return fmt.Errorf("rightsizing: MemOver (%.1f) must be less than MemUnder (%.1f)", t.MemOver, t.MemUnder)
	}
	return nil
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
	maxVal := sorted[len(sorted)-1]

	return Stats{Avg: avg, P95: p95, Max: maxVal}
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
