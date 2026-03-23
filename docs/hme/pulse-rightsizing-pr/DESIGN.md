# Right-Sizing Feature for Pulse — Design Document

**Date:** 2026-03-22
**Author:** cchapman (ported from ProxCenter)
**Target:** PR to [rcourtman/pulse](https://github.com/rcourtman/pulse)
**Status:** Approved design, ready for implementation

---

## 1. Overview

Add deterministic, algorithmic VM/container right-sizing analysis to Pulse. No AI, no LLM keys, no Pro license required for the core feature. Pure P95-based classification using Pulse's existing metrics history.

### What It Does

For every running guest (VM/container), compute the 95th-percentile of CPU and memory utilization over a user-selected time range, classify each metric independently, and present actionable verdicts: **idle**, **over-provisioned**, **right-sized**, **under-provisioned**, or **mixed**.

### Origin

This algorithm was developed for [ProxCenter](https://github.com/cchapman/proxcenter) and has been validated in production. The port adapts it from TypeScript/PostgreSQL to Go/SQLite, consuming Pulse's existing `metrics.db` persistent store.

---

## 2. Architecture Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Architecture | Standalone page + API | Accessible without AI config or Pro license |
| Data source | Pulse's existing `metrics.db` SQLite store | No new data collection, no extra PVE API calls |
| Computation | On-the-fly per API request | No new tables, no new goroutines, smallest PR footprint |
| Frontend | Dedicated SolidJS page | Full summary + sortable table + CSV export |
| Time ranges | 1h–30d with accuracy labels | Transparency over restriction; respects Pro license for >7d |

---

## 3. Classification Algorithm

### 3.1 P95 Computation

```
Given: sorted array of metric values (ascending)
Index: ceil(0.95 * length) - 1
P95 = values[index]
```

### 3.2 Thresholds (Defaults, All Configurable via Query Params)

| Metric | Idle | Over-provisioned | Right-sized | Under-provisioned |
|--------|------|-----------------|-------------|-------------------|
| **CPU P95** | < 5% | < 30% | 30–85% | > 85% |
| **Memory P95** | < 10% | < 30% | 30–90% | > 90% |

### 3.3 Classification Logic

```
ClassifyCPU(cpuP95):
  if cpuP95 < idle_threshold  → idle
  if cpuP95 > under_threshold → under-provisioned
  if cpuP95 < over_threshold  → over-provisioned
  else                        → right-sized

ClassifyMemory(memP95):
  (same logic with memory thresholds)

ClassifyOverall(cpuVerdict, memVerdict):
  if cpu == mem                                         → that verdict
  if either is insufficient-data                        → insufficient-data
  if one is idle and other is over                      → over-provisioned
  if one is right-sized and other is over/idle/under    → the non-right-sized verdict
  otherwise                                             → mixed
```

Examples:
| CPU      | Memory   | Overall          | Rationale                          |
|----------|----------|------------------|------------------------------------|
| idle     | idle     | idle             | same                               |
| over     | over     | over-provisioned | same                               |
| idle     | over     | over-provisioned | both indicate waste                |
| idle     | right    | idle             | non-right-sized verdict wins       |
| right    | over     | over-provisioned | non-right-sized verdict wins       |
| right    | under    | under-provisioned| non-right-sized verdict wins       |
| idle     | under    | mixed            | conflicting signals                |
| over     | under    | mixed            | conflicting signals                |

### 3.4 Streak Calculation

Walk backwards through the **daily** tier from today. Count consecutive days where the daily average produces the same overall verdict. Stop on first day that disagrees or on a data gap. This tells users "this VM has been idle for 14 consecutive days."

### 3.5 Minimum Samples

If a guest has fewer than `MinSamples` (default: 10) data points for the requested range, verdict = `insufficient-data`. This prevents false classifications from sparse data.

---

## 4. Time Range Support

### 4.1 Tier Selection

| Range | SQLite Tier | Expected Samples/Guest | Data Quality |
|-------|-------------|----------------------|--------------|
| 1h | minute | ~60 | high |
| 6h | minute | ~360 | high |
| 24h | minute | ~1,440 | high |
| 7d | hourly | ~168 | good |
| 14d | daily | ~14 | low |
| 30d | daily | ~30 | low |

### 4.2 Data Quality Indicator

API response includes:
```json
{
  "range": "30d",
  "dataQuality": "low",
  "dataQualityNote": "Based on daily averages — verdicts are directional, not precise",
  "tier": "daily",
  "sampleCount": 28
}
```

### 4.3 Licensing

Ranges >7d require `license.FeatureLongTermMetrics` (Pulse Pro). Free tier limited to 1h–7d. API returns 402 with upgrade message for unlicensed long-range requests — same pattern as existing `handleMetricsHistory`.

---

## 5. Backend Design

### 5.1 Package Structure

```
internal/rightsizing/
  classify.go         — Pure classification functions (zero dependencies)
  classify_test.go    — Unit tests for all verdict paths
  analyzer.go         — Orchestrates metrics queries + classification
  analyzer_test.go    — Integration tests with mock metrics store
```

### 5.2 `classify.go` — Pure Functions

```go
package rightsizing

type Verdict string

const (
    VerdictIdle             Verdict = "idle"
    VerdictOverProvisioned  Verdict = "over-provisioned"
    VerdictRightSized       Verdict = "right-sized"
    VerdictUnderProvisioned Verdict = "under-provisioned"
    VerdictMixed            Verdict = "mixed"
    VerdictInsufficientData Verdict = "insufficient-data"
)

type Thresholds struct {
    CPUIdle  float64 // default: 5
    CPUOver  float64 // default: 30
    CPUUnder float64 // default: 85
    MemIdle  float64 // default: 10
    MemOver  float64 // default: 30
    MemUnder float64 // default: 90
    MinSamples int   // default: 10
}

func DefaultThresholds() Thresholds
func ComputeP95(values []float64) float64
func ComputeStats(values []float64) Stats  // avg, p95, max
func ClassifyCPU(cpuP95 float64, t Thresholds) Verdict
func ClassifyMemory(memP95 float64, t Thresholds) Verdict
func ClassifyOverall(cpu, mem Verdict) Verdict
```

Entirely testable with zero imports beyond `math` and `sort`.

### 5.3 `analyzer.go` — Orchestration

```go
type GuestResult struct {
    ID           string  `json:"id"`
    Name         string  `json:"name"`
    Node         string  `json:"node"`
    Type         string  `json:"type"`     // "vm" or "container"
    VMID         int     `json:"vmid"`
    Status       string  `json:"status"`
    CPUs         int     `json:"cpus"`
    MaxMem       int64   `json:"maxMem"`   // bytes
    CPUAvg       float64 `json:"cpuAvg"`
    CPUP95       float64 `json:"cpuP95"`
    CPUMax       float64 `json:"cpuMax"`
    MemAvg       float64 `json:"memAvg"`
    MemP95       float64 `json:"memP95"`
    MemMax       float64 `json:"memMax"`
    CPUVerdict   Verdict `json:"cpuVerdict"`
    MemVerdict   Verdict `json:"memVerdict"`
    Overall      Verdict `json:"overall"`
    DaysAtVerdict int    `json:"daysAtVerdict"`
    SampleCount  int     `json:"sampleCount"`
}

type Summary struct {
    TotalGuests     int     `json:"totalGuests"`
    Idle            int     `json:"idle"`
    OverProvisioned int     `json:"overProvisioned"`
    RightSized      int     `json:"rightSized"`
    UnderProvisioned int    `json:"underProvisioned"`
    Mixed           int     `json:"mixed"`
    InsufficientData int    `json:"insufficientData"`
    ReclaimableMemGB float64 `json:"reclaimableMemoryGB"`
    ReclaimableCPUs  int     `json:"reclaimableCPUs"`
}

type Result struct {
    Summary         Summary       `json:"summary"`
    Guests          []GuestResult `json:"guests"`
    Range           string        `json:"range"`
    Tier            string        `json:"tier"`
    DataQuality     string        `json:"dataQuality"`
    DataQualityNote string        `json:"dataQualityNote,omitempty"`
    ComputeTimeMs   int64         `json:"computeTimeMs"`
}

// MetricsQuerier abstracts metrics.Store for testing
type MetricsQuerier interface {
    Query(resourceType, resourceID, metricType string,
          start, end time.Time, stepSecs int64) ([]metrics.MetricPoint, error)
}

func Analyze(querier MetricsQuerier, state models.StateSnapshot,
             t Thresholds, timeRange string) (*Result, error)
```

**Key implementation details:**
- Filter guests: `status == "running"` AND `template == false`
- Use `getTenantMonitor(ctx)` for multi-tenancy support
- Batch queries where possible (iterate guests, query per guest — SQLite WAL handles concurrent reads efficiently)
- Reclaimable memory: for over-provisioned guests, `reclaimable = (maxMem - maxMem * memP95/100) * 0.8` (keep 20% headroom above P95)
- Reclaimable CPUs: for over-provisioned guests, `reclaimable = cpus - max(1, ceil(cpus * cpuP95/100))` (keep at least 1 core)

### 5.4 API Endpoints

**`GET /api/rightsizing`**
Query params (all optional):
- `range` — `1h|6h|24h|7d|14d|30d` (default: `7d`)
- `threshold_cpu_idle` — float 0-100 (default: 5)
- `threshold_cpu_over` — float 0-100 (default: 30)
- `threshold_cpu_under` — float 0-100 (default: 85)
- `threshold_mem_idle` — float 0-100 (default: 10)
- `threshold_mem_over` — float 0-100 (default: 30)
- `threshold_mem_under` — float 0-100 (default: 90)

Returns `Result` JSON.

**`GET /api/rightsizing/export`**
Same query params. Returns `text/csv` with all guest details. CSV formula injection protection applied.

### 5.5 API Wiring

In `internal/api/router.go`:
```go
mux.HandleFunc("/api/rightsizing", r.handleRightSizing)
mux.HandleFunc("/api/rightsizing/export", r.handleRightSizingExport)
```

Handler implementation in a new file `internal/api/rightsizing_handlers.go` to keep the diff clean.

---

## 6. Frontend Design

### 6.1 New Files

```
frontend-modern/src/pages/RightSizing.tsx
frontend-modern/src/components/RightSizing/
  SummaryCards.tsx
  GuestTable.tsx
  VerdictBadge.tsx
  ExportButton.tsx
frontend-modern/src/api/rightsizing.ts
```

### 6.2 Page Layout

**Summary Cards (top row):**
5 stat cards following Pulse's existing card design:
- Idle (gray) — count + percentage
- Over-provisioned (blue) — count + percentage
- Right-sized (green) — count + percentage
- Under-provisioned (amber) — count + percentage
- Reclaimable Resources (purple) — memory GB + vCPU cores

**Data quality banner** (shown when `dataQuality == "low"`):
> ⓘ 30-day analysis uses daily averages. For precise verdicts, use 7d or shorter.

**Toolbar:**
- Range selector: `1h | 6h | 24h | 7d | 14d | 30d`
- CSV export button
- Verdict filter (dropdown or toggle chips)
- Search by guest name

**Guest Table:**
Sortable columns: Name, Node, Type, VMID, CPU P95, Mem P95, CPU Verdict, Mem Verdict, Overall Verdict, Days at Verdict.

Verdict values rendered as colored pill badges (`VerdictBadge` component).

Clicking a row opens Pulse's existing guest drawer with history charts for the selected time range.

### 6.3 Navigation

Add "Right-Sizing" entry to the sidebar navigation (`frontend-modern/src/components/Layout/Sidebar.tsx`).

Icon: `Scale` from Lucide (already available in Pulse).

### 6.4 No New Dependencies

Uses existing: TailwindCSS, Lucide icons, SolidJS primitives, `createResource` for data fetching.

---

## 7. Error Handling & Edge Cases

| Scenario | Handling |
|----------|----------|
| Guest has < MinSamples data points | Verdict = `insufficient-data`, gray badge |
| Empty metrics.db (fresh install) | Empty results, `totalGuests: 0`, helpful message |
| Guest started recently (partial data) | `sampleCount` per guest shown; MinSamples gate applies |
| Stopped guests | Excluded from analysis (only `status == "running"`) |
| Templates | Excluded (`template == false` filter) |
| Metric gaps (null RRD points) | Already filtered by Pulse during metrics write |
| Large fleet (2000+ guests) | SQLite WAL concurrent reads; `X-Compute-Time-Ms` header |
| CSV formula injection | Prefix `=`, `+`, `-`, `@` cells with tab character |
| Invalid threshold params | 400 with descriptive validation error |
| Unlicensed >7d range | 402 with upgrade message (same as existing pattern) |
| Multi-tenancy | Use `getTenantMonitor(ctx)` for correct monitor instance |
| metrics.db corrupted/missing | 500 with clear error message; Pulse keeps running |

---

## 8. Testing Strategy

### 8.1 Unit Tests — `classify_test.go`
- All verdict paths for CPU classification
- All verdict paths for memory classification
- Overall classification matrix (all combinations)
- P95 computation: empty, single value, odd/even lengths, all-same values
- Threshold validation: valid, invalid, edge cases

### 8.2 Integration Tests — `analyzer_test.go`
- Mock `MetricsQuerier` with controlled data
- Verify summary counts
- Verify reclaimable resource calculations
- Verify streak computation (consecutive days, gaps, transitions)
- Verify MinSamples gate
- Verify template/stopped guest exclusion
- Verify tier selection per range

### 8.3 API Tests — `rightsizing_handlers_test.go`
- Valid requests with default params
- Custom threshold params
- Invalid range
- Invalid thresholds (validation)
- License gating for >7d
- CSV export format and formula injection protection
- Empty state (no guests)

### 8.4 Frontend Tests
- Follow Pulse's existing test patterns (if any)
- Verify verdict badge renders correct colors
- Verify sort functionality
- Verify range selector triggers re-fetch

---

## 9. Design Attack Results

**Issues found and fixed:**
1. **Templates** — Must filter `template == false` when iterating guests. Added to analyzer spec.
2. **Multi-tenancy** — Must use `getTenantMonitor()` not a hardcoded monitor reference. Matches existing Pulse patterns.

**Rubber-duck check:** Data flows cleanly from request → state snapshot → metrics query → P95 → classification → JSON response. No resource lifecycle concerns (read-only, no new allocations).

**Concurrency check:** SQLite WAL allows unlimited concurrent readers. State snapshot is a copy. Safe.

**Best practices check:** Separation of concerns (pure functions / orchestration / HTTP handler), no new dependencies, consistent with existing Pulse patterns.

**Verdict: Design attack passed — two minor issues caught and incorporated.**

---

## 10. ProxCenter Algorithm Reference

The classification algorithm is ported from these ProxCenter source files:

- `apps/api/src/utils/metrics.ts` — `computeStats()`, `classifyCpu()`, `classifyMem()`, `classifyOverall()`
- `apps/api/src/routes/infrastructure.ts` — `getRightSizingData()`, streak SQL, reclaimable calculations, CSV export
- `apps/api/src/worker/jobs/poll-pve-server.ts` — Metric collection and verdict computation
- `apps/api/src/db/schema.ts` — `pveGuestMetrics` and `pveGuestMetricsHistory` tables

The full ported algorithm reference is in [ALGORITHM-REFERENCE.md](./ALGORITHM-REFERENCE.md).
