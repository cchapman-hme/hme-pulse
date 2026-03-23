package rightsizing

import (
	"strings"
	"testing"
	"time"

	"github.com/rcourtman/pulse-go-rewrite/internal/models"
	"github.com/rcourtman/pulse-go-rewrite/pkg/metrics"
)

// mockQuerier returns pre-configured metric data keyed by "resourceType:resourceID:metricType".
type mockQuerier struct {
	data map[string][]metrics.MetricPoint
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

	// 168 hour-sized points all at low usage → over-provisioned
	cpuValues := make([]float64, 168)
	memValues := make([]float64, 168)
	for i := range cpuValues {
		cpuValues[i] = 10.0 // 10% CPU → over-provisioned (5 < 10 < 30)
		memValues[i] = 15.0 // 15% memory → over-provisioned (10 < 15 < 30)
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
	if result.Summary.ReclaimableCPUs <= 0 {
		t.Fatalf("expected reclaimable CPUs > 0, got %d", result.Summary.ReclaimableCPUs)
	}
	if result.Guests[0].SampleCount != 168 {
		t.Fatalf("expected 168 samples, got %d", result.Guests[0].SampleCount)
	}
}

func TestAnalyze_InsufficientData(t *testing.T) {
	now := time.Now()
	// Only 2 points — below MinSamples=10
	q := &mockQuerier{data: map[string][]metrics.MetricPoint{
		"guest:test:vm2:cpu":    makePoints([]float64{50, 55}, now.Add(-2*time.Hour), time.Hour),
		"guest:test:vm2:memory": makePoints([]float64{60, 65}, now.Add(-2*time.Hour), time.Hour),
	}}

	state := models.StateSnapshot{
		VMs: []models.VM{{
			ID: "test:vm2", Name: "tiny", Node: "node1",
			VMID: 101, Status: "running", CPUs: 1,
			Memory: models.Memory{Total: 1024 * 1024 * 1024},
		}},
	}

	result, err := Analyze(q, state, DefaultThresholds(), "7d")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if result.Summary.TotalGuests != 1 {
		t.Fatalf("expected 1 guest, got %d", result.Summary.TotalGuests)
	}
	if result.Summary.InsufficientData != 1 {
		t.Fatalf("expected 1 insufficient-data, got %d", result.Summary.InsufficientData)
	}
	if result.Guests[0].Overall != VerdictInsufficientData {
		t.Fatalf("expected insufficient-data, got %v", result.Guests[0].Overall)
	}
	if result.Guests[0].SampleCount != 2 {
		t.Fatalf("expected 2 samples, got %d", result.Guests[0].SampleCount)
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

func TestAnalyze_TemplatesExcluded(t *testing.T) {
	q := &mockQuerier{data: map[string][]metrics.MetricPoint{}}
	state := models.StateSnapshot{
		VMs: []models.VM{
			{ID: "test:vm4", Name: "template-vm", Status: "running", CPUs: 2, Template: true},
		},
	}

	result, err := Analyze(q, state, DefaultThresholds(), "7d")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if result.Summary.TotalGuests != 0 {
		t.Fatalf("expected 0 guests (templates excluded), got %d", result.Summary.TotalGuests)
	}
}

func TestAnalyze_MixedVerdict(t *testing.T) {
	now := time.Now()
	start := now.Add(-7 * 24 * time.Hour)

	cpuValues := make([]float64, 168) // idle CPU (< 5)
	memValues := make([]float64, 168) // under-provisioned memory (> 90)
	for i := range cpuValues {
		cpuValues[i] = 3.0  // idle
		memValues[i] = 92.0 // under-provisioned
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
	if result.Guests[0].CPUVerdict != VerdictIdle {
		t.Fatalf("expected idle CPU, got %v", result.Guests[0].CPUVerdict)
	}
	if result.Guests[0].MemVerdict != VerdictUnderProvisioned {
		t.Fatalf("expected under-provisioned memory, got %v", result.Guests[0].MemVerdict)
	}
	if result.Guests[0].Overall != VerdictMixed {
		t.Fatalf("expected mixed (idle CPU + under-provisioned mem), got %v", result.Guests[0].Overall)
	}
}

func TestAnalyze_ContainersIncluded(t *testing.T) {
	now := time.Now()
	start := now.Add(-7 * 24 * time.Hour)

	cpuValues := make([]float64, 168)
	memValues := make([]float64, 168)
	for i := range cpuValues {
		cpuValues[i] = 50.0 // right-sized CPU
		memValues[i] = 50.0 // right-sized memory
	}

	q := &mockQuerier{data: map[string][]metrics.MetricPoint{
		"guest:test:ct1:cpu":    makePoints(cpuValues, start, time.Hour),
		"guest:test:ct1:memory": makePoints(memValues, start, time.Hour),
	}}

	state := models.StateSnapshot{
		Containers: []models.Container{{
			ID: "test:ct1", Name: "nginx", Node: "node1",
			VMID: 300, Status: "running", CPUs: 2,
			Memory: models.Memory{Total: 2 * 1024 * 1024 * 1024},
		}},
	}

	result, err := Analyze(q, state, DefaultThresholds(), "7d")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if result.Summary.TotalGuests != 1 {
		t.Fatalf("expected 1 guest (container), got %d", result.Summary.TotalGuests)
	}
	if result.Guests[0].Type != "container" {
		t.Fatalf("expected type 'container', got %q", result.Guests[0].Type)
	}
	if result.Guests[0].Overall != VerdictRightSized {
		t.Fatalf("expected right-sized, got %v", result.Guests[0].Overall)
	}
}

func TestAnalyze_UnknownRangeFallsBackTo7d(t *testing.T) {
	q := &mockQuerier{data: map[string][]metrics.MetricPoint{}}
	state := models.StateSnapshot{}

	result, err := Analyze(q, state, DefaultThresholds(), "banana")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if result.Range != "7d" {
		t.Fatalf("expected fallback to '7d', got %q", result.Range)
	}
}

func TestAnalyze_ResultFields(t *testing.T) {
	q := &mockQuerier{data: map[string][]metrics.MetricPoint{}}
	state := models.StateSnapshot{}

	result, err := Analyze(q, state, DefaultThresholds(), "14d")
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if result.Tier != "daily" {
		t.Fatalf("expected tier 'daily' for 14d, got %q", result.Tier)
	}
	if result.DataQuality != "low" {
		t.Fatalf("expected dataQuality 'low' for 14d, got %q", result.DataQuality)
	}
	if result.DataQualityNote == "" {
		t.Fatal("expected a non-empty DataQualityNote for 14d")
	}
	if result.ComputeTimeMs < 0 {
		t.Fatalf("expected non-negative ComputeTimeMs, got %d", result.ComputeTimeMs)
	}
}
