# Right-Sizing Algorithm — Ported Reference

**Source:** ProxCenter `apps/api/src/utils/metrics.ts` and `apps/api/src/routes/infrastructure.ts`
**Target:** Go implementation in `internal/rightsizing/`

This document contains the exact ProxCenter logic, annotated with Go porting notes.

---

## 1. Core Statistics: `computeStats()`

### ProxCenter (TypeScript)
```typescript
export function computeStats(values: number[]): { avg: number; p95: number; max: number } {
  if (values.length === 0) return { avg: 0, p95: 0, max: 0 };
  const sorted = [...values].sort((a, b) => a - b);
  const avg = sorted.reduce((a, b) => a + b, 0) / sorted.length;
  const p95 = sorted[Math.floor(0.95 * (sorted.length - 1))];
  const max = sorted[sorted.length - 1];
  return { avg, p95, max };
}
```

### Go Port
```go
package rightsizing

import (
    "math"
    "sort"
)

type Stats struct {
    Avg float64
    P95 float64
    Max float64
}

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

    p95Idx := int(math.Floor(0.95 * float64(len(sorted)-1)))
    p95 := sorted[p95Idx]

    max := sorted[len(sorted)-1]

    return Stats{Avg: avg, P95: p95, Max: max}
}

func ComputeP95(values []float64) float64 {
    return ComputeStats(values).P95
}
```

**Porting note:** The P95 index formula `Math.floor(0.95 * (length - 1))` is preserved exactly. For 168 values (7d hourly), this gives index 158 — the value at the 95th percentile.

---

## 2. CPU Classification: `classifyCpu()`

### ProxCenter (TypeScript)
```typescript
// cpuP95 is a 0-1 fraction (e.g. 0.15 = 15%)
export function classifyCpu(cpuP95: number): Verdict {
  if (cpuP95 < 0.05) return 'idle';
  if (cpuP95 > 0.85) return 'under-provisioned';
  if (cpuP95 < 0.30) return 'over-provisioned';
  return 'right-sized';
}
```

### Go Port
```go
// ClassifyCPU classifies CPU sizing based on P95 CPU usage.
// cpuP95 is a percentage (0-100).
// Note: ProxCenter uses 0-1 fractions. Pulse uses 0-100 percentages.
// Thresholds are adjusted accordingly.
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
```

**CRITICAL PORTING NOTE:**
- ProxCenter stores CPU as a **0-1 fraction** (PVE API native format)
- Pulse stores CPU as a **0-100 percentage** (converted during polling: `vm.CPU * 100`)
- Default thresholds must be adjusted: ProxCenter's `0.05` → Pulse's `5.0`

| Threshold | ProxCenter (0-1) | Pulse (0-100) |
|-----------|-----------------|---------------|
| CPU idle | 0.05 | 5.0 |
| CPU over | 0.30 | 30.0 |
| CPU under | 0.85 | 85.0 |

---

## 3. Memory Classification: `classifyMem()`

### ProxCenter (TypeScript)
```typescript
// memP95 is a 0-1 fraction (e.g. 0.45 = 45%)
export function classifyMem(memP95: number): Verdict {
  if (memP95 < 0.10) return 'idle';
  if (memP95 > 0.90) return 'under-provisioned';
  if (memP95 < 0.30) return 'over-provisioned';
  return 'right-sized';
}
```

### Go Port
```go
// ClassifyMemory classifies memory sizing based on P95 memory usage.
// memP95 is a percentage (0-100).
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
```

**Porting note:** Same 0-1 → 0-100 conversion applies. Pulse already stores `Memory.Usage` as 0-100.

| Threshold | ProxCenter (0-1) | Pulse (0-100) |
|-----------|-----------------|---------------|
| Mem idle | 0.10 | 10.0 |
| Mem over | 0.30 | 30.0 |
| Mem under | 0.90 | 90.0 |

---

## 4. Overall Classification: `classifyOverall()`

### ProxCenter (TypeScript)
```typescript
export function classifyOverall(cpuVerdict: Verdict, memVerdict: Verdict): Verdict {
  if (cpuVerdict === memVerdict) return cpuVerdict;
  const pair = new Set([cpuVerdict, memVerdict]);
  if (pair.has('idle') && pair.has('right-sized')) return 'right-sized';
  return 'mixed';
}
```

### Go Port
```go
// ClassifyOverall derives the overall verdict from independent CPU and memory verdicts.
// Enhancement over ProxCenter: richer combination rules based on DESIGN.md §3.3.
func ClassifyOverall(cpu, mem Verdict) Verdict {
    if cpu == mem {
        return cpu
    }

    // If either is insufficient-data, the overall is insufficient-data
    if cpu == VerdictInsufficientData || mem == VerdictInsufficientData {
        return VerdictInsufficientData
    }

    // idle + over-provisioned → over-provisioned (both indicate over-allocation)
    if (cpu == VerdictIdle && mem == VerdictOverProvisioned) ||
       (cpu == VerdictOverProvisioned && mem == VerdictIdle) {
        return VerdictOverProvisioned
    }

    // right-sized + anything else → the non-right-sized verdict
    if cpu == VerdictRightSized {
        return mem
    }
    if mem == VerdictRightSized {
        return cpu
    }

    return VerdictMixed
}
```

**Enhancement over ProxCenter:** ProxCenter only handles `idle+right-sized → right-sized` and falls through to `mixed` for everything else. The Pulse port implements the full design-doc logic:
- `idle + right-sized → idle` (non-right-sized wins)
- `idle + over → over` (both indicate waste, over is more actionable)
- `right + over/under → the non-right-sized verdict`
- All conflicting signals (e.g. `over + under`) → mixed

---

## 5. Reclaimable Resources Calculation

### ProxCenter (TypeScript)
```typescript
// Reclaimable memory:
if ((r.memVerdict === 'over-provisioned' || r.memVerdict === 'idle') && r.memoryBytes && r.memP95 != null) {
    const usedMem = Number(r.memoryBytes) * r.memP95;  // memP95 is 0-1 fraction
    reclaimableMemory += Number(r.memoryBytes) - usedMem;
}

// Reclaimable CPUs:
if ((r.cpuVerdict === 'over-provisioned' || r.cpuVerdict === 'idle') && r.cpuCores && r.cpuP95 != null) {
    const needed = Math.ceil(r.cpuCores * r.cpuP95 / 0.50);  // assume 50% target utilization
    reclaimableCpuCores += r.cpuCores - Math.max(needed, 1);  // keep at least 1 core
}
```

### Go Port
```go
// computeReclaimableMemory calculates reclaimable memory for an over-provisioned guest.
// memP95 is 0-100 percentage, maxMem is bytes.
// Retains 20% headroom above P95 before treating the remainder as reclaimable.
func computeReclaimableMemory(memP95 float64, maxMem int64) int64 {
    usedBytes := float64(maxMem) * (memP95 / 100.0) // convert percentage to fraction
    reclaimable := (float64(maxMem) - usedBytes) * 0.8 // keep 20% headroom
    if reclaimable < 0 {
        return 0
    }
    return int64(reclaimable)
}

// computeReclaimableCPUs calculates reclaimable CPU cores for an over-provisioned guest.
// cpuP95 is 0-100 percentage, cpus is the number of configured vCPU cores.
func computeReclaimableCPUs(cpuP95 float64, cpus int) int {
    targetUtilization := 0.50 // assume 50% target per core
    needed := int(math.Ceil(float64(cpus) * (cpuP95 / 100.0) / targetUtilization))
    if needed < 1 {
        needed = 1
    }
    reclaimable := cpus - needed
    if reclaimable < 0 {
        return 0
    }
    return reclaimable
}
```

**Porting note:** The 0-1 → 0-100 conversion affects the reclaimable memory formula. ProxCenter multiplies `memoryBytes * memP95` where memP95 is 0-1. In Pulse, we must divide by 100.

---

## 6. Streak Calculation (Days at Verdict)

### ProxCenter (PostgreSQL)
```sql
SELECT h.pve_server_id, h.vmid, count(*)::integer AS streak_days
FROM (
  SELECT pve_server_id, vmid, verdict, recorded_date,
         recorded_date - (ROW_NUMBER() OVER (
           PARTITION BY pve_server_id, vmid, verdict ORDER BY recorded_date
         ) * interval '1 day') AS grp
  FROM pve_guest_metrics_history
) h
JOIN pve_guest_metrics m ON m.pve_server_id = h.pve_server_id AND m.vmid = h.vmid
WHERE h.verdict = m.verdict
GROUP BY h.pve_server_id, h.vmid, h.grp
ORDER BY h.pve_server_id, h.vmid, max(h.recorded_date) DESC
```

### Go Port (Iterative, using Pulse's daily metrics)

Pulse doesn't have a separate history table with daily verdicts. Instead, we compute the streak algorithmically:

```go
// streakDays calculates consecutive days at the current verdict.
// It calls QueryAll once to fetch all 90 days of daily CPU+memory data in two
// round-trips rather than four separate Query calls.
// currentVerdict is the verdict computed from the main analysis range.
func streakDays(
    querier MetricsQuerier,
    resourceID string,
    currentVerdict Verdict,
    t Thresholds,
) int {
    end := time.Now()
    start := end.Add(-90 * 24 * time.Hour)
    // stepSecs=86400 → one point per day
    allPoints, err := querier.QueryAll("guest", resourceID, start, end, 86400)
    if err != nil {
        return 0
    }

    cpuPts := allPoints["cpu"]
    memPts := allPoints["memory"]
    if len(cpuPts) == 0 {
        return 0
    }

    // Build a map of date → memory value for O(1) lookup
    memByDate := make(map[string]float64, len(memPts))
    for _, p := range memPts {
        memByDate[p.Timestamp.UTC().Format("2006-01-02")] = p.Value
    }

    streak := 0
    prevDate := time.Time{}

    // cpuPts are sorted ascending by timestamp; iterate newest-first
    for i := len(cpuPts) - 1; i >= 0; i-- {
        pt := cpuPts[i]
        dateKey := pt.Timestamp.UTC().Format("2006-01-02")
        memVal, hasMemory := memByDate[dateKey]
        if !hasMemory {
            break // missing memory data for this day — stop
        }

        cpuV := ClassifyCPU(pt.Value, t)  // daily avg as approximation
        memV := ClassifyMemory(memVal, t)
        dayVerdict := ClassifyOverall(cpuV, memV)

        if dayVerdict != currentVerdict {
            break // streak broken
        }

        // Check for continuity (no gaps > 2 days)
        if !prevDate.IsZero() {
            gap := prevDate.Sub(pt.Timestamp)
            if gap > 48*time.Hour {
                break
            }
        }
        prevDate = pt.Timestamp
        streak++
    }

    return streak
}
```

**Porting note:** ProxCenter uses a gap-and-islands SQL pattern against a dedicated `pve_guest_metrics_history` table that stores explicit daily verdicts (CPU+memory together). Pulse has no such table. Instead, `streakDays` calls `QueryAll` once to retrieve 90 days of daily CPU and memory averages together (two SQLite round-trips), then recomputes each day's verdict inline. ProxCenter's daily verdict was derived from that day's high-resolution data; ours comes from the daily average — an acceptable approximation that requires zero new tables or schema changes.

**Difference from ProxCenter:** The function was called `computeStreak` in ProxCenter. Renamed to `streakDays` in Pulse to better reflect its return type (int, days) and to avoid confusion with generic "streak" helpers.

---

## 7. Data Access: Pulse's `metrics.Store` Interface

### Available Interface
```go
// From pkg/metrics/store.go

// Query returns points for a single metric type.
func (s *Store) Query(
    resourceType string,   // "guest" or "vm"
    resourceID string,     // e.g. "pve1:node1:100"
    metricType string,     // "cpu", "memory"
    start, end time.Time,
    stepSecs int64,        // 0 = native tier resolution
) ([]MetricPoint, error)

// QueryAll returns points for ALL metric types in a single call.
// Returns map[metricType][]MetricPoint.
func (s *Store) QueryAll(
    resourceType string,
    resourceID string,
    start, end time.Time,
    stepSecs int64,
) (map[string][]MetricPoint, error)
```

### How Right-Sizing Uses It

Right-sizing uses `QueryAll` (not `Query`) to reduce SQLite round-trips through the single connection:

```go
// For each running guest — one QueryAll call instead of two Query calls:
end := time.Now()
start := end.Add(-7 * 24 * time.Hour) // 7d range

allPoints, err := querier.QueryAll("guest", guest.ID, start, end, 0)
if err != nil {
    // skip guest
    continue
}

cpuPoints := allPoints["cpu"]
memPoints := allPoints["memory"]

cpuValues := make([]float64, len(cpuPoints))
for i, p := range cpuPoints {
    cpuValues[i] = p.Value // already 0-100 percentage
}

memValues := make([]float64, len(memPoints))
for i, p := range memPoints {
    memValues[i] = p.Value
}

cpuStats := ComputeStats(cpuValues)
memStats := ComputeStats(memValues)

cpuVerdict := ClassifyCPU(cpuStats.P95, thresholds)
memVerdict := ClassifyMemory(memStats.P95, thresholds)
overall := ClassifyOverall(cpuVerdict, memVerdict)
```

**Why QueryAll?** `metrics.Store` uses `SetMaxOpenConns(1)` (single SQLite connection). With 200 guests, two `Query()` calls per guest = 400 sequential DB round-trips. `QueryAll` halves this to 200 for main analysis, and streak gating (only runs for 7d+ ranges) eliminates streak queries entirely for 1h/6h/24h requests.

### Tier Selection by Range
```go
func tierForRange(r string) (duration time.Duration, stepSecs int64) {
    switch r {
    case "1h":
        return time.Hour, 60           // minute tier
    case "6h":
        return 6 * time.Hour, 60       // minute tier
    case "24h":
        return 24 * time.Hour, 60      // minute tier
    case "7d":
        return 7 * 24 * time.Hour, 0   // hourly tier (auto)
    case "14d":
        return 14 * 24 * time.Hour, 0  // daily tier (auto)
    case "30d":
        return 30 * 24 * time.Hour, 0  // daily tier (auto)
    default:
        return 7 * 24 * time.Hour, 0   // default to 7d
    }
}
```

---

## 8. CSV Export

### ProxCenter (TypeScript)
```typescript
const csvEscape = (val: string) => {
    // Neutralise Excel/Sheets formula injection
    const safe = /^[=+\-@]/.test(val) ? `\t${val}` : val;
    if (safe.includes(',') || safe.includes('"') || safe.includes('\n'))
        return '"' + safe.replace(/"/g, '""') + '"';
    return safe;
};

const header = 'VMID,Name,Type,Cluster,CPU Cores,CPU Avg %,CPU P95 %,Mem Avg %,Mem P95 %,' +
               'CPU Verdict,Mem Verdict,Overall Verdict,Days at Verdict\r\n';
```

### Go Port
```go
func csvEscape(val string) string {
    // Neutralise Excel/Sheets formula injection
    if len(val) > 0 && (val[0] == '=' || val[0] == '+' || val[0] == '-' || val[0] == '@') {
        val = "\t" + val
    }
    if strings.ContainsAny(val, ",\"\n") {
        return `"` + strings.ReplaceAll(val, `"`, `""`) + `"`
    }
    return val
}

var csvHeader = "VMID,Name,Type,Node,CPUs,Max Mem (GB),CPU Avg %,CPU P95 %," +
    "Mem Avg %,Mem P95 %,CPU Verdict,Mem Verdict,Overall,Days at Verdict,Samples\r\n"
```

**Enhancement:** Added "Node", "Max Mem (GB)", and "Samples" columns not in ProxCenter's original.

---

## 9. Threshold Defaults Summary

### Complete Defaults (Go)
```go
func DefaultThresholds() Thresholds {
    return Thresholds{
        CPUIdle:    5.0,   // P95 CPU < 5% → idle
        CPUOver:    30.0,  // P95 CPU < 30% → over-provisioned
        CPUUnder:   85.0,  // P95 CPU > 85% → under-provisioned
        MemIdle:    10.0,  // P95 Mem < 10% → idle
        MemOver:    30.0,  // P95 Mem < 30% → over-provisioned
        MemUnder:   90.0,  // P95 Mem > 90% → under-provisioned
        MinSamples: 10,    // Need ≥10 data points to classify
    }
}
```

### Classification Order (Important!)

The if-statement order matters. For both CPU and memory:
1. Check idle first (lowest threshold)
2. Check under-provisioned next (highest threshold)
3. Check over-provisioned (middle threshold)
4. Default: right-sized

This ordering ensures that a CPU at 3% hits "idle" (< 5%) before it could hit "over-provisioned" (< 30%).
