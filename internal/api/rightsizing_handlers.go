package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rcourtman/pulse-go-rewrite/internal/license"
	"github.com/rcourtman/pulse-go-rewrite/internal/rightsizing"
)

// rightSizingParams holds the validated, parsed parameters shared by the JSON
// and CSV right-sizing endpoints.
type rightSizingParams struct {
	timeRange  string
	thresholds rightsizing.Thresholds
}

// validRightSizingRanges is the canonical set of accepted range strings.
var validRightSizingRanges = map[string]bool{
	"1h": true, "6h": true, "24h": true,
	"7d": true, "14d": true, "30d": true,
}

// parseRightSizingParams validates and extracts query parameters common to
// handleRightSizing and handleRightSizingExport.  It writes an HTTP error and
// returns false if validation fails.
func parseRightSizingParams(w http.ResponseWriter, req *http.Request) (rightSizingParams, bool) {
	query := req.URL.Query()

	timeRange := query.Get("range")
	if timeRange == "" {
		timeRange = "7d"
	}
	if !validRightSizingRanges[timeRange] {
		http.Error(w, "Invalid range. Valid: 1h, 6h, 24h, 7d, 14d, 30d", http.StatusBadRequest)
		return rightSizingParams{}, false
	}

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

	if err := rightsizing.ValidateThresholds(th); err != nil {
		http.Error(w, fmt.Sprintf("Invalid thresholds: %v", err), http.StatusBadRequest)
		return rightSizingParams{}, false
	}

	return rightSizingParams{timeRange: timeRange, thresholds: th}, true
}

// writeLicenseError writes a 402 Payment Required JSON response for long-range
// right-sizing requests that lack a Pulse Pro license.
func writeLicenseError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":       "license_required",
		"message":     "14d/30d right-sizing requires a Pulse Pro license",
		"feature":     license.FeatureLongTermMetrics,
		"upgrade_url": "https://pulserelay.pro/",
		"max_free":    "7d",
	})
}

// handleRightSizing returns a JSON right-sizing analysis for all running guests.
// Query parameters:
//   - range: 1h|6h|24h|7d|14d|30d (default: 7d)
//   - threshold_cpu_idle, threshold_cpu_over, threshold_cpu_under: CPU thresholds (0-100)
//   - threshold_mem_idle, threshold_mem_over, threshold_mem_under: Memory thresholds (0-100)
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

	params, ok := parseRightSizingParams(w, req)
	if !ok {
		return
	}

	// License gate: >7d ranges require Pulse Pro; mirrors handleMetricsHistory.
	if params.timeRange == "14d" || params.timeRange == "30d" {
		if !r.licenseHandlers.Service(req.Context()).HasFeature(license.FeatureLongTermMetrics) {
			writeLicenseError(w)
			return
		}
	}

	state := monitor.GetState()
	metricsStore := monitor.GetMetricsStore()
	if metricsStore == nil {
		http.Error(w, "Metrics store not available", http.StatusServiceUnavailable)
		return
	}

	result, err := rightsizing.Analyze(metricsStore, state, params.thresholds, params.timeRange)
	if err != nil {
		http.Error(w, fmt.Sprintf("Analysis failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Compute-Time-Ms", strconv.FormatInt(result.ComputeTimeMs, 10))
	_ = json.NewEncoder(w).Encode(result)
}

// handleRightSizingExport returns a CSV export of the right-sizing analysis.
// Supports the same query parameters as handleRightSizing, including threshold
// overrides, so the CSV is guaranteed to match what the UI displays.
func (r *Router) handleRightSizingExport(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	monitor := r.getTenantMonitor(req.Context())
	if monitor == nil {
		http.Error(w, "Monitor not available", http.StatusInternalServerError)
		return
	}

	params, ok := parseRightSizingParams(w, req)
	if !ok {
		return
	}

	// License gate: same as handleRightSizing.
	if params.timeRange == "14d" || params.timeRange == "30d" {
		if !r.licenseHandlers.Service(req.Context()).HasFeature(license.FeatureLongTermMetrics) {
			writeLicenseError(w)
			return
		}
	}

	state := monitor.GetState()
	metricsStore := monitor.GetMetricsStore()
	if metricsStore == nil {
		http.Error(w, "Metrics store not available", http.StatusServiceUnavailable)
		return
	}

	result, err := rightsizing.Analyze(metricsStore, state, params.thresholds, params.timeRange)
	if err != nil {
		http.Error(w, fmt.Sprintf("Analysis failed: %v", err), http.StatusInternalServerError)
		return
	}

	// csvEscape prevents formula injection (OWASP CSV injection) and handles quoting.
	// The tab-prefix approach is the most widely compatible mitigation for spreadsheet tooling.
	csvEscape := func(val string) string {
		if len(val) > 0 && (val[0] == '=' || val[0] == '+' || val[0] == '-' || val[0] == '@') {
			val = "\t" + val
		}
		if strings.ContainsAny(val, ",\"\n\r") {
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

	// Include the range in the filename so exports from different time windows
	// don't collide when saved to the same directory.
	filename := fmt.Sprintf("right-sizing-%s.csv", params.timeRange)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_, _ = w.Write([]byte(sb.String()))
}
