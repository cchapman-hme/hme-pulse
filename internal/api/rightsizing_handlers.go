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

	query := req.URL.Query()
	timeRange := query.Get("range")
	if timeRange == "" {
		timeRange = "7d"
	}

	// Validate range.
	validRanges := map[string]bool{
		"1h": true, "6h": true, "24h": true,
		"7d": true, "14d": true, "30d": true,
	}
	if !validRanges[timeRange] {
		http.Error(w, "Invalid range. Valid: 1h, 6h, 24h, 7d, 14d, 30d", http.StatusBadRequest)
		return
	}

	// License check for >7d ranges — mirrors the pattern in handleMetricsHistory.
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

	// Parse optional threshold overrides.
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

	// Validate threshold ordering after user overrides.
	if err := rightsizing.ValidateThresholds(th); err != nil {
		http.Error(w, fmt.Sprintf("Invalid thresholds: %v", err), http.StatusBadRequest)
		return
	}

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

// handleRightSizingExport returns a CSV export of the right-sizing analysis.
// Supports the same query parameters as handleRightSizing.
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

	query := req.URL.Query()
	timeRange := query.Get("range")
	if timeRange == "" {
		timeRange = "7d"
	}

	// License check mirrors handleRightSizing.
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

	// csvEscape prevents CSV injection and handles quoting.
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
