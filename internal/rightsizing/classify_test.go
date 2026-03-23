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
	// sorted: [10, 30, 50, 70, 90]; P95 index = floor(0.95*4) = 3 → 70.0
	s := ComputeStats([]float64{50, 10, 90, 30, 70})
	if s.Max != 90.0 {
		t.Fatalf("max: want 90.0, got %f", s.Max)
	}
	if s.P95 != 70.0 {
		t.Fatalf("p95: want 70.0, got %f", s.P95)
	}
}

func TestComputeStats_DoesNotMutateInput(t *testing.T) {
	original := []float64{50, 10, 90, 30, 70}
	input := make([]float64, len(original))
	copy(input, original)
	ComputeStats(input)
	for i, v := range input {
		if v != original[i] {
			t.Fatalf("ComputeStats mutated input at index %d: got %f, want %f", i, v, original[i])
		}
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

func TestClassifyOverall_Commutativity(t *testing.T) {
	// Only valid CPU/memory input verdicts — VerdictMixed is an output only
	verdicts := []Verdict{
		VerdictIdle, VerdictOverProvisioned, VerdictRightSized,
		VerdictUnderProvisioned, VerdictInsufficientData,
	}
	for _, cpu := range verdicts {
		for _, mem := range verdicts {
			fwd := ClassifyOverall(cpu, mem)
			rev := ClassifyOverall(mem, cpu)
			if fwd != rev {
				t.Errorf("ClassifyOverall not commutative: (%v,%v)=%v but (%v,%v)=%v",
					cpu, mem, fwd, mem, cpu, rev)
			}
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

func TestValidateThresholds_Valid(t *testing.T) {
	if err := ValidateThresholds(DefaultThresholds()); err != nil {
		t.Fatalf("DefaultThresholds() should pass validation: %v", err)
	}
}

func TestValidateThresholds_Invalid(t *testing.T) {
	tests := []struct {
		name string
		th   Thresholds
	}{
		{"CPUIdle >= CPUOver", Thresholds{CPUIdle: 30, CPUOver: 30, CPUUnder: 85, MemIdle: 10, MemOver: 30, MemUnder: 90}},
		{"CPUOver >= CPUUnder", Thresholds{CPUIdle: 5, CPUOver: 85, CPUUnder: 85, MemIdle: 10, MemOver: 30, MemUnder: 90}},
		{"MemIdle >= MemOver", Thresholds{CPUIdle: 5, CPUOver: 30, CPUUnder: 85, MemIdle: 30, MemOver: 30, MemUnder: 90}},
		{"MemOver >= MemUnder", Thresholds{CPUIdle: 5, CPUOver: 30, CPUUnder: 85, MemIdle: 10, MemOver: 90, MemUnder: 90}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateThresholds(tt.th); err == nil {
				t.Errorf("expected validation error for %s, got nil", tt.name)
			}
		})
	}
}
