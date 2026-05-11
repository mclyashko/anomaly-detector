package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
)

// UI provides minimal web interface for incident management.
type UI struct {
	svc         *core.NotifierService
	logger      *slog.Logger
	analyzerURL string
}

// New creates a new UI handler.
func New(svc *core.NotifierService, logger *slog.Logger, analyzerURL string) *UI {
	return &UI{svc: svc, logger: logger, analyzerURL: analyzerURL}
}

// HandleList renders the incident list page with filters and pagination.
func (u *UI) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page := 1
	pageSize := 20

	if p := r.URL.Query().Get("page"); p != "" {
		if parsed := parsePositiveInt(p); parsed > 0 {
			page = parsed
		}
	}

	statusVals := r.URL.Query()["status"]
	severityVals := r.URL.Query()["severity"]

	var incidents []core.Incident
	var total int

	if len(statusVals) == 0 && len(severityVals) == 0 {
		var err error
		incidents, err = u.svc.ListIncidents(ctx)
		if err != nil {
			u.logger.Error("failed to list incidents", "err", err)
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}
		total = len(incidents)
	} else {
		var filters core.IncidentFilters
		for _, s := range statusVals {
			if st := core.IncidentStatus(s); st == core.StatusOpen || st == core.StatusUpdated || st == core.StatusEscalated || st == core.StatusResolved {
				filters.Status = append(filters.Status, st)
			}
		}
		filters.Severity = severityVals
		var err error
		incidents, total, err = u.svc.ListIncidentsFiltered(ctx, filters, page, pageSize)
		if err != nil {
			u.logger.Error("failed to list filtered incidents", "err", err)
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}
	}

	data := struct {
		Incidents    []core.Incident
		Total        int
		Page         int
		PageSize     int
		StatusVals   []string
		SeverityVals []string
	}{
		Incidents:    incidents,
		Total:        total,
		Page:         page,
		PageSize:     pageSize,
		StatusVals:   statusVals,
		SeverityVals: severityVals,
	}

	if err := listTemplate.Execute(w, data); err != nil {
		u.logger.Error("failed to render list template", "err", err)
	}
}

// HandleDetail renders the incident detail page.
func (u *UI) HandleDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	incident, err := u.svc.GetIncident(r.Context(), id)
	if err != nil {
		if err == core.ErrIncidentNotFound {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		u.logger.Error("failed to get incident", "err", err, "id", id)
		http.Error(w, "Internal Error", http.StatusInternalServerError)
		return
	}

	data := struct {
		Incident *core.IncidentWithDetails
	}{Incident: incident}

	if err := detailTemplate.Execute(w, data); err != nil {
		u.logger.Error("failed to render detail template", "err", err)
	}
}

// payloadInfo holds parsed anomaly event data for display.
type payloadInfo struct {
	Rule     string  `json:"rule"`
	Value    float64 `json:"value"`
	Forecast float64 `json:"forecast"`
	LowerCI  float64 `json:"lower_ci"`
	UpperCI  float64 `json:"upper_ci"`
	Message  string  `json:"message"`
	Severity string  `json:"severity"`
}

// parsePayload converts JSON payload bytes to a readable struct.
func parsePayload(data []byte) *payloadInfo {
	var p payloadInfo
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	return &p
}

// stringifyPayload converts JSON bytes to a formatted string for fallback display.
func stringifyPayload(data []byte) string {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return string(data)
	}
	out := ""
	for k, v := range m {
		out += k + ": " + fmt.Sprintf("%v", v) + "\n"
	}
	return out
}

// buildChart generates an inline SVG chart from incident events.
// Shows confidence interval ribbon, forecast line, and actual value points.
func buildChart(events []core.IncidentEvent) template.HTML {
	if len(events) == 0 {
		return ""
	}

	type point struct {
		x        float64
		value    float64
		forecast float64
		lowerCI  float64
		upperCI  float64
	}

	var points []point
	for i, e := range events {
		p := parsePayload(e.Payload)
		if p == nil {
			continue
		}
		pt := point{
			x:        float64(i),
			value:    p.Value,
			forecast: p.Forecast,
			lowerCI:  p.LowerCI,
			upperCI:  p.UpperCI,
		}
		// Normalize to avoid division by zero
		if pt.lowerCI == 0 && pt.upperCI == 0 {
			pt.lowerCI = pt.value * 0.9
			pt.upperCI = pt.value * 1.1
		}
		points = append(points, pt)
	}

	if len(points) == 0 {
		return ""
	}

	// Compute bounding box for all values
	minVal := points[0].lowerCI
	maxVal := points[0].upperCI
	for _, p := range points {
		if p.value < minVal {
			minVal = p.value
		}
		if p.forecast < minVal {
			minVal = p.forecast
		}
		if p.lowerCI < minVal {
			minVal = p.lowerCI
		}
		if p.value > maxVal {
			maxVal = p.value
		}
		if p.forecast > maxVal {
			maxVal = p.forecast
		}
		if p.upperCI > maxVal {
			maxVal = p.upperCI
		}
	}

	// Add padding
	rangeVal := maxVal - minVal
	if rangeVal < 0.001 {
		rangeVal = 0.001
		minVal -= 0.05
		maxVal += 0.05
		rangeVal = maxVal - minVal
	}

	const W, H float64 = 600, 180
	const padLeft, padRight, padTop, padBottom float64 = 40, 20, 20, 30

	xScale := func(x float64) float64 {
		return padLeft + (x/float64(len(points)-1))*(W-padLeft-padRight)
	}
	yScale := func(y float64) float64 {
		return H - padBottom - ((y-minVal)/rangeVal)*(H-padTop-padBottom)
	}

	// Build CI ribbon path
	ribbonPath := ""
	for i, p := range points {
		x := xScale(p.x)
		yTop := yScale(p.upperCI)
		if i == 0 {
			ribbonPath += fmt.Sprintf("M %.2f %.2f", x, yTop)
		} else {
			ribbonPath += fmt.Sprintf(" L %.2f %.2f", x, yTop)
		}
	}
	for i := len(points) - 1; i >= 0; i-- {
		p := points[i]
		x := xScale(p.x)
		yBot := yScale(p.lowerCI)
		ribbonPath += fmt.Sprintf(" L %.2f %.2f", x, yBot)
	}
	ribbonPath += " Z"

	// Build forecast line
	forecastPath := ""
	hasForecast := false
	for i, p := range points {
		if p.forecast == 0 {
			continue
		}
		hasForecast = true
		x := xScale(p.x)
		y := yScale(p.forecast)
		if i == 0 {
			forecastPath += fmt.Sprintf("M %.2f %.2f", x, y)
		} else {
			forecastPath += fmt.Sprintf(" L %.2f %.2f", x, y)
		}
	}

	// Build value points
	pointsSVG := ""
	for _, p := range points {
		x := xScale(p.x)
		y := yScale(p.value)
		color := "#ef5350" // red for anomaly
		pointsSVG += fmt.Sprintf(`<circle cx="%.2f" cy="%.2f" r="5" fill="%s" stroke="#fff" stroke-width="1.5"/>`,
			x, y, color)
		// Label
		pointsSVG += fmt.Sprintf(`<text x="%.2f" y="%.2f" text-anchor="middle" dy="-8" fill="%s" font-size="10">%.4f</text>`,
			x, y, color, p.value)
	}

	// X-axis labels (timestamps)
	xLabels := ""
	for i, e := range events {
		if i%(len(events)/4+1) == 0 || i == len(events)-1 {
			x := xScale(float64(i))
			xLabels += fmt.Sprintf(`<text x="%.2f" y="%.0f" text-anchor="middle" fill="var(--text-secondary)" font-size="11">%s</text>`,
				x, H-padBottom+15, e.Timestamp.Format("15:04"))
		}
	}

	return template.HTML(fmt.Sprintf(`<svg viewBox="0 0 %.0f %.0f" xmlns="http://www.w3.org/2000/svg" style="width:100%%;max-width:640px;margin:15px 0;">
  <defs>
    <linearGradient id="ciGrad" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%%" stop-color="var(--primary)" stop-opacity="0.2"/>
      <stop offset="100%%" stop-color="var(--primary)" stop-opacity="0.05"/>
    </linearGradient>
  </defs>
  <!-- CI ribbon -->
  <path d="%s" fill="url(#ciGrad)" stroke="var(--primary)" stroke-width="0.5" stroke-opacity="0.3"/>
  <!-- Forecast line -->
  %s
  <!-- Value points -->
  %s
  <!-- X-axis labels -->
  %s
  <!-- Axes -->
  <line x1="%.2f" y1="0" x2="%.2f" y2="%.2f" stroke="var(--border)" stroke-width="1"/>
  <line x1="%.2f" y1="%.2f" x2="%.0f" y2="%.2f" stroke="var(--border)" stroke-width="1"/>
</svg>`, W, H,
		ribbonPath,
		map[bool]string{true: fmt.Sprintf(`<polyline points="%s" fill="none" stroke="var(--primary)" stroke-width="1.5" stroke-dasharray="4,2" opacity="0.6"/>`, forecastPath), false: ""}[hasForecast],
		pointsSVG,
		xLabels,
		padLeft, padLeft, H-padBottom,
		padLeft, H-padBottom, W, H-padBottom))
}

// buildPagination builds pagination info.
func buildPagination(page, pageSize, total int) map[string]any {
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	return map[string]any{
		"page":       page,
		"pageSize":   pageSize,
		"total":      total,
		"totalPages": totalPages,
		"hasPrev":    page > 1,
		"hasNext":    page < totalPages,
	}
}

// templateFuncs provides functions available in templates.
var templateFuncs = template.FuncMap{
	"parsePayload":     parsePayload,
	"stringifyPayload": stringifyPayload,
	"buildChart":       buildChart,
	"pagination":       buildPagination,
	"safeHTML":         safeHTML,
	"add":              func(a, b int) int { return a + b },
	"sub":              func(a, b int) int { return a - b },
	"statusIcon": func(s core.IncidentStatus) string {
		switch s {
		case core.StatusOpen:
			return "⚪"
		case core.StatusUpdated:
			return "🟡"
		case core.StatusEscalated:
			return "🔴"
		case core.StatusResolved:
			return "🟢"
		default:
			return "⚪"
		}
	},
	"severityIcon": func(s string) string {
		switch s {
		case "critical":
			return "⚠️"
		case "warning":
			return "⚡"
		case "info":
			return "ℹ️"
		default:
			return "📊"
		}
	},
	"contains": func(s []string, val string) bool {
		for _, v := range s {
			if v == val {
				return true
			}
		}
		return false
	},
	"queryWith": func(key, value, existing string) string {
		if existing == "" {
			return key + "=" + value
		}
		return existing + "&" + key + "=" + value
	},
}

func parsePositiveInt(s string) int {
	n, _ := strconv.Atoi(s)
	if n < 1 {
		n = 1
	}
	return n
}

var listTemplate = template.Must(template.New("list").Funcs(templateFuncs).Parse(`<!DOCTYPE html>
<html data-theme="light">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1.0"/>
<title>Incidents</title>
<style>
*,*::before,*::after{box-sizing:border-box}
:root {
  --bg:#ffffff; --surface:#f8f9fa; --surface2:#f0f0f0; --text:#1a1a2e; --text-secondary:#666;
  --border:#e0e0e0; --primary:#1565c0; --primary-hover:#1e88e5;
  --shadow:0 2px 8px rgba(0,0,0,0.08); --shadow-hover:0 4px 16px rgba(0,0,0,0.12);
  --radius:8px; --radius-sm:4px;
  --status-open-bg:#ffffff; --status-open-text:#666666;
  --status-updated-bg:#fff3e0; --status-updated-text:#ef6c00;
  --status-escalated-bg:#ffebee; --status-escalated-text:#c62828;
  --status-resolved-bg:#e8f5e9; --status-resolved-text:#2e7d32;
  --severity-critical:#c62828; --severity-warning:#ef6c00; --severity-info:#1565c0;
  --toast-bg:#333; --toast-text:#fff;
  --spinner:#1565c0;
}
[data-theme="dark"] {
  --bg:#1a1a2e; --surface:#16213e; --surface2:#1f2b4a; --text:#e8e8e8; --text-secondary:#9999aa;
  --border:#2a2a4a; --primary:#4fc3f7; --primary-hover:#29b6f6;
  --shadow:0 2px 8px rgba(0,0,0,0.3); --shadow-hover:0 4px 16px rgba(0,0,0,0.4);
  --status-open-bg:#1e1e2e; --status-open-text:#aaaaaa;
  --status-updated-bg:#3d2e1f; --status-updated-text:#ffb74d;
  --status-escalated-bg:#3d1f1f; --status-escalated-text:#ef5350;
  --status-resolved-bg:#1f3d2e; --status-resolved-text:#66bb6a;
  --severity-critical:#ef5350; --severity-warning:#ffb74d; --severity-info:#4fc3f7;
  --toast-bg:#eee; --toast-text:#1a1a2e;
  --spinner:#4fc3f7;
}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:var(--bg);color:var(--text);margin:0;min-height:100vh}
.wrap{max-width:1100px;margin:0 auto;padding:24px 20px}
.header{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px}
.header h1{margin:0;font-size:24px;font-weight:600}
.header a{color:var(--primary);text-decoration:none}

/* Theme toggle */
#theme-toggle{background:var(--surface);border:1px solid var(--border);border-radius:var(--radius);padding:8px 14px;cursor:pointer;font-size:16px;color:var(--text);transition:all .2s}
#theme-toggle:hover{background:var(--surface2)}

/* Filters bar */
.filters{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin-bottom:16px;padding:14px;background:var(--surface);border-radius:var(--radius);box-shadow:var(--shadow)}
.filters label{font-size:13px;color:var(--text-secondary);font-weight:500}
.filters select{background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);padding:6px 10px;font-size:13px;cursor:pointer}
.filters select:focus{outline:none;border-color:var(--primary)}
.filters .filter-group{display:flex;align-items:center;gap:6px}
.filters button{background:var(--primary);color:#fff;border:none;border-radius:var(--radius-sm);padding:6px 14px;font-size:13px;cursor:pointer;transition:opacity .2s}
.filters button:hover{opacity:.85}
.filters .count-badge{background:var(--primary);color:#fff;border-radius:10px;padding:2px 8px;font-size:11px;margin-left:4px}

/* Table */
.incidents-table{width:100%;border-collapse:separate;border-spacing:0;border-radius:var(--radius);overflow:hidden;box-shadow:var(--shadow);background:var(--surface)}
.incidents-table th{background:var(--surface2);color:var(--text-secondary);font-weight:500;font-size:12px;text-transform:uppercase;letter-spacing:.5px;padding:12px 14px;text-align:left;border-bottom:1px solid var(--border)}
.incidents-table td{padding:12px 14px;border-bottom:1px solid var(--border, #f0);vertical-align:middle}
.incidents-table tbody tr{transition:background .15s;cursor:pointer}
.incidents-table tbody tr:hover{background:var(--surface2)}
.incidents-table tbody tr:last-child td{border-bottom:none}
.status-badge{display:inline-flex;align-items:center;gap:5px;padding:4px 10px;border-radius:var(--radius-sm);font-size:12px;font-weight:500}
.status-OPEN{background:var(--status-open-bg);color:var(--status-open-text)}
.status-UPDATED{background:var(--status-updated-bg);color:var(--status-updated-text)}
.status-ESCALATED{background:var(--status-escalated-bg);color:var(--status-escalated-text)}
.status-RESOLVED{background:var(--status-resolved-bg);color:var(--status-resolved-text)}
.sev-critical{color:var(--severity-critical);font-weight:600}
.sev-warning{color:var(--severity-warning);font-weight:500}
.sev-info{color:var(--severity-info)}
.code{font-family:monospace;font-size:11px;color:var(--text-secondary);background:var(--surface2);padding:2px 6px;border-radius:var(--radius-sm)}
.td-id{font-family:monospace;font-size:12px;color:var(--text-secondary);max-width:120px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.td-time{font-size:12px;color:var(--text-secondary);white-space:nowrap}
a.view-link{color:var(--primary);text-decoration:none;font-weight:500;font-size:13px}
a.view-link:hover{text-decoration:underline}

/* Actions row */
.td-actions{text-align:right}

/* Loading */
#loading{display:none;align-items:center;justify-content:center;padding:40px;color:var(--text-secondary)}
#loading.active{display:flex;gap:10px}
.spinner{width:20px;height:20px;border:2px solid var(--border);border-top-color:var(--spinner);border-radius:50%;animation:spin .7s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
.no-data td{text-align:center;padding:40px;color:var(--text-secondary);font-size:14px}

/* Pagination */
.pagination{display:flex;align-items:center;justify-content:space-between;margin-top:16px;padding:12px;background:var(--surface);border-radius:var(--radius)}
.pagination-info{font-size:13px;color:var(--text-secondary)}
.pagination-controls{display:flex;gap:8px;align-items:center}
.pagination-controls button{background:var(--surface2);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);padding:6px 12px;cursor:pointer;font-size:13px;transition:all .15s}
.pagination-controls button:hover:not(:disabled){background:var(--primary);color:#fff;border-color:var(--primary)}
.pagination-controls button:disabled{opacity:.4;cursor:not-allowed}
.page-num{font-size:13px;color:var(--text);padding:0 8px}

/* Toast */
#toast-container{position:fixed;bottom:20px;right:20px;z-index:9999;display:flex;flex-direction:column;gap:8px}
.toast{padding:12px 18px;border-radius:var(--radius);font-size:14px;font-weight:500;box-shadow:0 4px 12px rgba(0,0,0,.15);animation:slideIn .3s ease;max-width:300px}
.toast-success{background:#2e7d32;color:#fff}
.toast-error{background:#c62828;color:#fff}
.toast-info{background:var(--primary);color:#fff}
@keyframes slideIn{from{transform:translateX(100%);opacity:0}to{transform:translateX(0);opacity:1}}

/* Real-time indicator */
.rt-indicator{display:inline-flex;align-items:center;gap:6px;font-size:12px;color:var(--text-secondary)}
.rt-dot{width:8px;height:8px;border-radius:50%;background:#4caf50;animation:pulse 2s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.4}}

/* Add loading overlay */
.table-wrap{position:relative}
.table-wrap.fetching{opacity:.5;pointer-events:none}
</style>
</head>
<body>
<div id="toast-container"></div>
<div class="wrap">
  <div class="header">
    <div style="display:flex;align-items:center;gap:20px">
      <h1>Incidents</h1>
      <a href="/rules" style="color:var(--primary);text-decoration:none;font-size:14px;font-weight:500">Rules</a>
    </div>
    <div style="display:flex;align-items:center;gap:16px">
      <span class="rt-indicator"><span class="rt-dot"></span>Auto-refresh 10s</span>
      <button id="theme-toggle" title="Toggle dark mode">🌙</button>
    </div>
  </div>

  <div class="filters">
    <div class="filter-group">
      <label>Status</label>
      <select id="filter-status" multiple size="2">
        <option value="OPEN" {{if contains .StatusVals "OPEN"}}selected{{end}}>⚪ OPEN</option>
        <option value="UPDATED" {{if contains .StatusVals "UPDATED"}}selected{{end}}>🟡 UPDATED</option>
        <option value="ESCALATED" {{if contains .StatusVals "ESCALATED"}}selected{{end}}>🔴 ESCALATED</option>
        <option value="RESOLVED" {{if contains .StatusVals "RESOLVED"}}selected{{end}}>🟢 RESOLVED</option>
      </select>
    </div>
    <div class="filter-group">
      <label>Severity</label>
      <select id="filter-severity">
        <option value="">All</option>
        <option value="critical" {{if contains .SeverityVals "critical"}}selected{{end}}>⚠️ critical</option>
        <option value="warning" {{if contains .SeverityVals "warning"}}selected{{end}}>⚡ warning</option>
        <option value="info" {{if contains .SeverityVals "info"}}selected{{end}}>ℹ️ info</option>
      </select>
    </div>
    <div class="filter-group">
      <label>Page size</label>
      <select id="filter-pagesize">
        <option value="10" {{if eq .PageSize 10}}selected{{end}}>10</option>
        <option value="20" {{if eq .PageSize 20}}selected{{end}}>20</option>
        <option value="50" {{if eq .PageSize 50}}selected{{end}}>50</option>
      </select>
    </div>
    <button onclick="applyFilters()">Apply</button>
    <span class="count-badge">{{.Total}} total</span>
  </div>

  <div class="table-wrap" id="table-wrap">
    <div id="loading"><div class="spinner"></div><span>Loading...</span></div>
    <table class="incidents-table">
      <thead>
        <tr>
          <th>ID</th>
          <th>Status</th>
          <th>Service</th>
          <th>Rule</th>
          <th>Metric</th>
          <th>Severity</th>
          <th>Updated</th>
          <th></th>
        </tr>
      </thead>
      <tbody id="incidents-body">
        {{range .Incidents}}
        <tr data-id="{{.ID}}" onclick="window.location='/incidents/' + this.dataset.id" style="cursor:pointer">
          <td class="td-id"><span class="code">{{.ID}}</span></td>
          <td><span class="status-badge status-{{.Status}}">{{statusIcon .Status}} {{.Status}}</span></td>
          <td>{{.Service}}</td>
          <td>{{.Rule}}</td>
          <td>{{.Metric}}</td>
          <td class="sev-{{.Severity}}">{{severityIcon .Severity}} {{.Severity}}</td>
          <td class="td-time">{{.UpdatedAt.Format "2006-01-02 15:04"}}</td>
          <td class="td-actions"><a href="#" class="view-link" onclick="event.stopPropagation();window.location='/incidents/' + this.closest('tr').dataset.id">View →</a></td>
        </tr>
        {{else}}
        <tr class="no-data"><td colspan="8">No incidents found — metrics are still accumulating or filters returned empty result.</td></tr>
        {{end}}
      </tbody>
    </table>
  </div>

  <div style="display:flex;justify-content:flex-end;margin-bottom:12px">
    <button onclick="exportCSV()" title="Export filtered results as CSV" style="background:var(--surface);border:1px solid var(--border);border-radius:var(--radius-sm);padding:6px 14px;cursor:pointer;font-size:13px;color:var(--text);display:inline-flex;align-items:center;gap:6px">📥 Export CSV</button>
  </div>

  {{$p := pagination .Page .PageSize .Total}}
  <div class="pagination">
    <span class="pagination-info">Showing page {{.Page}} of {{$p.totalPages}} ({{.Total}} incidents)</span>
    <div class="pagination-controls">
      <button onclick="goToPage({{.Page}} - 1)" {{if not $p.hasPrev}}disabled{{end}}>← Prev</button>
      <span class="page-num">Page {{.Page}}</span>
      <button onclick="goToPage({{.Page}} + 1)" {{if not $p.hasNext}}disabled{{end}}>Next →</button>
    </div>
  </div>
</div>

<script>
(function(){
  // Theme
  var theme = localStorage.getItem('theme') || 'light';
  document.documentElement.setAttribute('data-theme', theme);
  document.getElementById('theme-toggle').textContent = theme === 'dark' ? '☀️' : '🌙';
  document.getElementById('theme-toggle').onclick = function(){
    theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', theme);
    localStorage.setItem('theme', theme);
    this.textContent = theme === 'dark' ? '☀️' : '🌙';
  };

  // Toast
  function showToast(msg, type){
    var c = document.getElementById('toast-container');
    var d = document.createElement('div');
    d.className = 'toast toast-' + type;
    d.textContent = msg;
    c.appendChild(d);
    setTimeout(function(){ d.style.opacity = '0'; setTimeout(function(){ d.remove(); }, 300); }, 3500);
  }
  window.showToast = showToast;

  // Filters
  function getFilters(){
    var statusSel = document.getElementById('filter-status');
    var statuses = [];
    for(var i=0;i<statusSel.options.length;i++){
      if(statusSel.options[i].selected) statuses.push(statusSel.options[i].value);
    }
    var severity = document.getElementById('filter-severity').value;
    var pageSize = document.getElementById('filter-pagesize').value;
    return {statuses: statuses, severity: severity, pageSize: parseInt(pageSize)};
  }

  function buildQuery(page){
    var f = getFilters();
    var params = [];
    f.statuses.forEach(function(s){ params.push('status=' + s); });
    if(f.severity) params.push('severity=' + f.severity);
    params.push('page=' + (page || 1));
    params.push('page_size=' + f.pageSize);
    return params.join('&');
  }

  window.applyFilters = function(){
    var f = getFilters();
    var q = [];
    f.statuses.forEach(function(s){ q.push('status=' + s); });
    if(f.severity) q.push('severity=' + f.severity);
    q.push('page=1');
    q.push('page_size=' + f.pageSize);
    location.search = q.join('&');
  };

  window.goToPage = function(n){
    if(n < 1) return;
    var f = getFilters();
    var q = [];
    f.statuses.forEach(function(s){ q.push('status=' + s); });
    if(f.severity) q.push('severity=' + f.severity);
    q.push('page=' + n);
    q.push('page_size=' + f.pageSize);
    location.search = q.join('&');
  };

  // Real-time refresh every 10s (only on page 1 without filters)
  var autoRefresh = {{len .StatusVals}} === 0 && {{len .SeverityVals}} === 0 && {{.Page}} === 1;
  if(autoRefresh){
    setInterval(function(){
      var wrap = document.getElementById('table-wrap');
      var loading = document.getElementById('loading');
      loading.classList.add('active');
      fetch('/api/v1/incidents?' + buildQuery(1))
        .then(function(r){ return r.json(); })
        .then(function(data){
          var prevCount = getActiveCount();
          renderTable(data.incidents);
          updateTitle(data.incidents);
          loading.classList.remove('active');
          var newCount = getActiveCount();
          if(data.incidents && data.incidents.length > 0 && newCount > prevCount){
            playBeep();
            showToast('New incident detected!', 'success');
          }
        })
        .catch(function(){
          loading.classList.remove('active');
        });
    }, 10000);
  }

  function renderTable(incidents){
    var tbody = document.getElementById('incidents-body');
    if(!incidents || incidents.length === 0){
      tbody.innerHTML = '<tr class="no-data"><td colspan="8">No incidents found</td></tr>';
      return;
    }
    var html = '';
    incidents.forEach(function(inc){
      var sevClass = 'sev-' + inc.severity;
      var statusBadgeClass = 'status-badge status-' + inc.status;
      var statusIcon = {'OPEN':'⚪','UPDATED':'🟡','ESCALATED':'🔴','RESOLVED':'🟢'}[inc.status] || '⚪';
      var sevIcon = {'critical':'⚠️','warning':'⚡','info':'ℹ️'}[inc.severity] || '📊';
      var date = new Date(inc.updated_at).toLocaleString('en-CA',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'}).replace(',','');
      html += '<tr onclick="window.location=\'/incidents/' + inc.id + '\'" style="cursor:pointer">' +
        '<td class="td-id"><span class="code">' + inc.id + '</span></td>' +
        '<td><span class="' + statusBadgeClass + '">' + statusIcon + ' ' + inc.status + '</span></td>' +
        '<td>' + escHtml(inc.service) + '</td>' +
        '<td>' + escHtml(inc.rule) + '</td>' +
        '<td>' + escHtml(inc.metric) + '</td>' +
        '<td class="' + sevClass + '">' + sevIcon + ' ' + inc.severity + '</td>' +
        '<td class="td-time">' + date + '</td>' +
        '<td class="td-actions"><a href="/incidents/' + inc.id + '" class="view-link" onclick="event.stopPropagation()">View →</a></td>' +
        '</tr>';
    });
    tbody.innerHTML = html;
  }

  function escHtml(s){
    if(!s) return '';
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
  }

  // Active incident count (OPEN, UPDATED, ESCALATED)
  function getActiveCount(){
    var tbody = document.getElementById('incidents-body');
    var rows = tbody.querySelectorAll('tr');
    var count = 0;
    rows.forEach(function(r){
      var badge = r.querySelector('.status-badge');
      if(badge && (badge.classList.contains('status-OPEN') || badge.classList.contains('status-UPDATED') || badge.classList.contains('status-ESCALATED'))){
        count++;
      }
    });
    return count;
  }

  // Update page title with active count
  function updateTitle(incidents){
    if(!incidents) return;
    var active = 0;
    incidents.forEach(function(inc){
      if(inc.status === 'OPEN' || inc.status === 'UPDATED' || inc.status === 'ESCALATED') active++;
    });
    document.title = active > 0 ? 'Incidents (' + active + ')' : 'Incidents';
  }

  // Web audio beep (no external files)
  function playBeep(){
    try {
      var ctx = new (window.AudioContext || window.webkitAudioContext)();
      var osc = ctx.createOscillator();
      var gain = ctx.createGain();
      osc.connect(gain);
      gain.connect(ctx.destination);
      osc.frequency.value = 880;
      osc.type = 'sine';
      gain.gain.setValueAtTime(0.15, ctx.currentTime);
      gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.4);
      osc.start(ctx.currentTime);
      osc.stop(ctx.currentTime + 0.4);
    } catch(e){}
  }

  // CSV export
  window.exportCSV = function(){
    var tbody = document.getElementById('incidents-body');
    var rows = tbody.querySelectorAll('tr');
    var csv = 'ID,Status,Service,Rule,Metric,Severity,Updated\n';
    rows.forEach(function(r){
      var cells = r.querySelectorAll('td');
      if(cells.length < 7) return;
      csv += '"' + escHtml(cells[0].textContent.trim()) + '","' + escHtml(cells[1].textContent.trim()) + '","' + escHtml(cells[2].textContent.trim()) + '","' + escHtml(cells[3].textContent.trim()) + '","' + escHtml(cells[4].textContent.trim()) + '","' + escHtml(cells[5].textContent.trim()) + '","' + escHtml(cells[6].textContent.trim()) + '"\n';
    });
    var blob = new Blob([csv], {type:'text/csv'});
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = 'incidents_' + new Date().toISOString().slice(0,19).replace(/:/g,'-') + '.csv';
    a.click();
    URL.revokeObjectURL(url);
    showToast('CSV exported', 'success');
  };

  // Initial title update with current page data
  var currentPageIncidents = [{{range .Incidents}}{status:'{{.Status}}'}{{end}}];
  updateTitle(currentPageIncidents);
})();
</script>
</body>
</html>`))

// HandleRules renders a page listing all active rules from the analyzer.
func (u *UI) HandleRules(w http.ResponseWriter, r *http.Request) {
	resp, err := http.Get(u.analyzerURL + "/api/v1/rules")
	if err != nil {
		u.logger.Warn("failed to fetch rules", "err", err)
		http.Error(w, "Analyzer unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Failed to load rules", resp.StatusCode)
		return
	}

	body, _ := io.ReadAll(resp.Body)
	var rules []map[string]any
	if err := json.Unmarshal(body, &rules); err != nil {
		http.Error(w, "Invalid response from analyzer", http.StatusInternalServerError)
		return
	}

	data := struct {
		Rules []map[string]any
	}{Rules: rules}

	if err := rulesTemplate.Execute(w, data); err != nil {
		u.logger.Error("failed to render rules template", "err", err)
	}
}

var rulesTemplate = template.Must(template.New("rules").Funcs(templateFuncs).Parse(`<!DOCTYPE html>
<html data-theme="light">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1.0"/>
<title>Rules</title>
<style>
*,*::before,*::after{box-sizing:border-box}
:root {
  --bg:#ffffff; --surface:#f8f9fa; --surface2:#f0f0f0; --text:#1a1a2e; --text-secondary:#666;
  --border:#e0e0e0; --primary:#1565c0; --shadow:0 2px 8px rgba(0,0,0,0.08);
  --radius:8px; --radius-sm:4px;
  --type-threshold:#ef6c00; --type-lua:#9c27b0; --type-ml:#1565c0; --type-ks:#c62828;
}
[data-theme="dark"] {
  --bg:#1a1a2e; --surface:#16213e; --surface2:#1f2b4a; --text:#e8e8e8; --text-secondary:#9999aa;
  --border:#2a2a4a; --primary:#4fc3f7; --shadow:0 2px 8px rgba(0,0,0,0.3);
  --type-threshold:#ffb74d; --type-lua:#ce93d8; --type-ml:#4fc3f7; --type-ks:#ef5350;
}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:var(--bg);color:var(--text);margin:0;min-height:100vh}
.wrap{max-width:960px;margin:0 auto;padding:24px 20px}
.header{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px}
.header h1{margin:0;font-size:24px;font-weight:600}
.back a{color:var(--primary);text-decoration:none;font-size:14px;font-weight:500;display:inline-flex;align-items:center;gap:6px}
.back a:hover{text-decoration:underline}
#theme-toggle{background:var(--surface);border:1px solid var(--border);border-radius:var(--radius);padding:8px 14px;cursor:pointer;font-size:16px;transition:all .2s}
#theme-toggle:hover{background:var(--surface2)}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:16px}
.card{background:var(--surface);border-radius:var(--radius);padding:18px;box-shadow:var(--shadow);border:1px solid var(--border);transition:box-shadow .2s}
.card:hover{box-shadow:0 4px 16px rgba(0,0,0,.12)}
.card-header{display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:12px;gap:10px}
.card-title{font-size:15px;font-weight:600;margin:0;color:var(--text)}
.type-badge{padding:3px 10px;border-radius:var(--radius-sm);font-size:12px;font-weight:600;text-transform:uppercase}
.type-threshold{background:#fff3e0;color:var(--type-threshold)}
.type-lua{background:#f3e5f5;color:var(--type-lua)}
.type-ml{background:#e3f2fd;color:var(--type-ml)}
.type-ks{background:#ffebee;color:var(--type-ks)}
.card-field{margin:6px 0}
.card-field label{font-size:11px;color:var(--text-secondary);font-weight:500;display:block;margin-bottom:2px;text-transform:uppercase;letter-spacing:.3px}
.card-field span{font-size:13px;font-weight:500}
.enabled-badge{padding:3px 8px;border-radius:var(--radius-sm);font-size:11px;font-weight:600}
.enabled-true{background:#e8f5e9;color:#2e7d32}
.enabled-false{background:#ffebee;color:#c62828}
.sev-critical{color:#c62828;font-weight:700}
.sev-warning{color:#ef6c00;font-weight:600}
.sev-info{color:#1565c0}
.no-rules{text-align:center;padding:60px;color:var(--text-secondary);font-size:16px}
.count-badge{background:var(--primary);color:#fff;border-radius:12px;padding:4px 12px;font-size:13px}
</style>
</head>
<body>
<div class="wrap">
  <div class="back"><a href="/">&larr; Back to Incidents</a></div>
  <div class="header">
    <h1>Active Rules <span class="count-badge">{{len .Rules}}</span></h1>
    <button id="theme-toggle" title="Toggle dark mode">🌙</button>
  </div>
  {{if .Rules}}
  <div class="grid">
    {{range .Rules}}
    <div class="card">
      <div class="card-header">
        <h3 class="card-title">{{.name}}</h3>
        <span class="type-badge type-{{.type}}">{{.type}}</span>
      </div>
      <div class="card-field"><label>Agent ID</label><span>{{.agent_id}}</span></div>
      <div class="card-field"><label>Metric</label><span>{{.metric}}</span></div>
      <div class="card-field"><label>Severity</label><span class="sev-{{.severity}}">{{.severity}}</span></div>
      <div class="card-field"><label>Status</label>
        {{if .enabled}}<span class="enabled-badge enabled-true">● Enabled</span>{{else}}<span class="enabled-badge enabled-false">○ Disabled</span>{{end}}
      </div>
    </div>
    {{end}}
  </div>
  {{else}}
  <div class="no-rules">No rules found — analyzer may be unavailable or no rules are configured.</div>
  {{end}}
</div>
<script>
var theme = localStorage.getItem('theme') || 'light';
document.documentElement.setAttribute('data-theme', theme);
document.getElementById('theme-toggle').textContent = theme === 'dark' ? '☀️' : '🌙';
document.getElementById('theme-toggle').onclick = function(){
  theme = theme === 'dark' ? 'light' : 'dark';
  document.documentElement.setAttribute('data-theme', theme);
  localStorage.setItem('theme', theme);
  this.textContent = theme === 'dark' ? '☀️' : '🌙';
};
</script>
</body>
</html>`))

var detailTemplate = template.Must(template.New("detail").Funcs(templateFuncs).Parse(`<!DOCTYPE html>
<html data-theme="light">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1.0"/>
<title>Incident</title>
<style>
*,*::before,*::after{box-sizing:border-box}
:root {
  --bg:#ffffff; --surface:#f8f9fa; --surface2:#f0f0f0; --text:#1a1a2e; --text-secondary:#666;
  --border:#e0e0e0; --primary:#1565c0; --primary-hover:#1e88e5;
  --shadow:0 2px 8px rgba(0,0,0,0.08); --shadow-hover:0 4px 16px rgba(0,0,0,.12);
  --radius:8px; --radius-sm:4px;
  --status-open-bg:#ffffff; --status-open-text:#666666;
  --status-updated-bg:#fff3e0; --status-updated-text:#ef6c00;
  --status-escalated-bg:#ffebee; --status-escalated-text:#c62828;
  --status-resolved-bg:#e8f5e9; --status-resolved-text:#2e7d32;
  --severity-critical:#c62828; --severity-warning:#ef6c00; --severity-info:#1565c0;
  --toast-success:#2e7d32; --toast-error:#c62828;
}
[data-theme="dark"] {
  --bg:#1a1a2e; --surface:#16213e; --surface2:#1f2b4a; --text:#e8e8e8; --text-secondary:#9999aa;
  --border:#2a2a4a; --primary:#4fc3f7; --primary-hover:#29b6f6;
  --shadow:0 2px 8px rgba(0,0,0,0.3); --shadow-hover:0 4px 16px rgba(0,0,0,.4);
  --status-open-bg:#1e1e2e; --status-open-text:#aaaaaa;
  --status-updated-bg:#3d2e1f; --status-updated-text:#ffb74d;
  --status-escalated-bg:#3d1f1f; --status-escalated-text:#ef5350;
  --status-resolved-bg:#1f3d2e; --status-resolved-text:#66bb6a;
  --severity-critical:#ef5350; --severity-warning:#ffb74d; --severity-info:#4fc3f7;
  --toast-success:#66bb6a; --toast-error:#ef5350;
}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:var(--bg);color:var(--text);margin:0;min-height:100vh}
.wrap{max-width:960px;margin:0 auto;padding:24px 20px}
.header{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px;gap:16px}
.header h1{margin:0;font-size:22px;font-weight:600;flex:1}
.status-badge{display:inline-flex;align-items:center;gap:6px;padding:6px 14px;border-radius:var(--radius-sm);font-size:14px;font-weight:600}
.status-OPEN{background:var(--status-open-bg);color:var(--status-open-text)}
.status-UPDATED{background:var(--status-updated-bg);color:var(--status-updated-text)}
.status-ESCALATED{background:var(--status-escalated-bg);color:var(--status-escalated-text)}
.status-RESOLVED{background:var(--status-resolved-bg);color:var(--status-resolved-text)}
.back{margin-bottom:20px}
.back a{color:var(--primary);text-decoration:none;font-size:14px;font-weight:500;display:inline-flex;align-items:center;gap:6px}
.back a:hover{text-decoration:underline}
.meta{background:var(--surface);border-radius:var(--radius);padding:20px;margin-bottom:20px;box-shadow:var(--shadow)}
.meta-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:16px;margin-bottom:16px}
.meta-item label{display:block;font-size:11px;color:var(--text-secondary);text-transform:uppercase;letter-spacing:.5px;margin-bottom:4px;font-weight:500}
.meta-item span{font-size:15px;font-weight:500;color:var(--text)}
.meta-item code{font-family:monospace;font-size:12px;background:var(--surface2);padding:2px 6px;border-radius:var(--radius-sm)}
.sev-critical{color:var(--severity-critical);font-weight:700}
.sev-warning{color:var(--severity-warning);font-weight:600}
.sev-info{color:var(--severity-info)}
.ml-card{background:linear-gradient(135deg,var(--surface) 0%,var(--surface2) 100%);border-radius:var(--radius);padding:18px 20px;margin:16px 0;border-left:4px solid var(--primary)}
.ml-card h3{font-size:13px;color:var(--primary);margin:0 0 14px 0;text-transform:uppercase;letter-spacing:.5px;font-weight:600}
.ml-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:14px;margin-bottom:12px}
.ml-item label{display:block;font-size:11px;color:var(--text-secondary);margin-bottom:3px;font-weight:500}
.ml-item span{font-size:16px;font-weight:600}
.ml-message{margin-top:10px}
.ml-message label{display:block;font-size:11px;color:var(--text-secondary);margin-bottom:4px;font-weight:500}
.ml-message p{font-family:monospace;font-size:13px;color:var(--text);margin:0;background:var(--bg);padding:10px;border-radius:var(--radius-sm);border:1px solid var(--border)}
.chart-section{background:var(--surface);border-radius:var(--radius);padding:20px;margin:20px 0;box-shadow:var(--shadow)}
.chart-section h3{font-size:13px;color:var(--text-secondary);margin:0 0 12px 0;text-transform:uppercase;letter-spacing:.5px;font-weight:500}
.no-chart{font-size:13px;color:var(--text-secondary);padding:20px;text-align:center}
.actions{display:flex;gap:10px;margin-top:20px}
button{transition:all .2s}
button.escalate{background:#ffebee;color:#c62828;border:none;border-radius:var(--radius-sm);padding:10px 20px;font-size:14px;font-weight:500;cursor:pointer}
button.escalate:hover{background:#ffcdd2}
button.resolve{background:#e8f5e9;color:#2e7d32;border:none;border-radius:var(--radius-sm);padding:10px 20px;font-size:14px;font-weight:500;cursor:pointer}
button.resolve:hover{background:#c8e6c9}
button:disabled{opacity:.4;cursor:not-allowed}
#resolve-form{margin-top:14px;display:none}
#resolve-form textarea{width:100%;padding:12px;border:1px solid var(--border);border-radius:var(--radius-sm);font-size:14px;background:var(--bg);color:var(--text);resize:vertical;min-height:80px;font-family:inherit}
#resolve-form textarea:focus{outline:none;border-color:var(--primary)}
.confirm-resolve{background:var(--toast-success);color:#fff;border:none;border-radius:var(--radius-sm);padding:8px 18px;font-size:13px;cursor:pointer;margin-top:8px}
.resolution{background:var(--status-resolved-bg);padding:14px 18px;border-radius:var(--radius);margin-top:16px}
.resolution label{font-size:11px;color:var(--text-secondary);text-transform:uppercase;font-weight:500;display:block;margin-bottom:4px}
.resolution p{margin:0;font-size:14px;color:var(--text)}
.section{margin-top:28px}
.section h2{font-size:15px;color:var(--text-secondary);text-transform:uppercase;letter-spacing:.5px;font-weight:600;margin-bottom:12px;border-bottom:1px solid var(--border);padding-bottom:8px}
.timeline{border-left:2px solid var(--border);padding-left:22px;margin-left:10px}
.event{position:relative;margin-bottom:18px;padding:14px;background:var(--surface);border-radius:var(--radius);box-shadow:var(--shadow);transition:box-shadow .2s}
.event:hover{box-shadow:var(--shadow-hover)}
.event::before{content:'';position:absolute;left:-27px;top:18px;width:10px;height:10px;border-radius:50%;background:var(--primary);border:2px solid var(--bg)}
.event-time{font-size:11px;color:var(--text-secondary);margin-bottom:8px;font-weight:500}
.event-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(140px,1fr));gap:8px}
.event-field{margin:2px 0}
.event-field label{font-size:11px;color:var(--text-secondary);font-weight:500;display:block}
.event-field span{font-size:13px;font-weight:500}
.event-msg{grid-column:1/-1;margin-top:8px}
.event-msg label{font-size:11px;color:var(--text-secondary);font-weight:500;display:block;margin-bottom:4px}
.event-msg span{font-family:monospace;font-size:12px;background:var(--bg);padding:8px;border-radius:var(--radius-sm);display:block;border:1px solid var(--border);color:var(--text)}
.comment{background:var(--surface);padding:12px 16px;border-radius:var(--radius);margin-bottom:10px;border-left:3px solid var(--primary)}
.comment-time{font-size:11px;color:var(--text-secondary);margin-bottom:6px;font-weight:500}
.comment p{margin:0;font-size:14px;line-height:1.5}
.add-comment{margin-top:16px}
.add-comment textarea{width:100%;padding:12px;border:1px solid var(--border);border-radius:var(--radius-sm);font-size:14px;background:var(--bg);color:var(--text);resize:vertical;min-height:80px;font-family:inherit}
.add-comment textarea:focus{outline:none;border-color:var(--primary)}
.submit-comment{background:var(--primary);color:#fff;border:none;border-radius:var(--radius-sm);padding:10px 22px;font-size:14px;cursor:pointer;margin-top:8px;transition:opacity .2s}
.submit-comment:hover{opacity:.85}
#theme-toggle{background:var(--surface);border:1px solid var(--border);border-radius:var(--radius);padding:8px 14px;cursor:pointer;font-size:16px;transition:all .2s}
#theme-toggle:hover{background:var(--surface2)}
#toast-container{position:fixed;bottom:20px;right:20px;z-index:9999;display:flex;flex-direction:column;gap:8px}
.toast{padding:12px 18px;border-radius:var(--radius);font-size:14px;font-weight:500;box-shadow:0 4px 12px rgba(0,0,0,.15);animation:slideIn .3s ease;max-width:320px}
.toast-success{background:var(--toast-success);color:#fff}
.toast-error{background:var(--toast-error);color:#fff}
@keyframes slideIn{from{transform:translateX(100%);opacity:0}to{transform:translateX(0);opacity:1}}
.spinner{width:24px;height:24px;border:3px solid var(--border);border-top-color:var(--primary);border-radius:50%;animation:spin .7s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
</style>
</head>
<body>
<div id="toast-container"></div>
<div class="wrap">

  <div class="back"><a href="/">&larr; Back to Incidents</a></div>

  <div class="header">
    <h1>Incident</h1>
    <div style="display:flex;align-items:center;gap:16px">
      <span class="status-badge status-{{.Incident.Status}}">{{statusIcon .Incident.Status}} {{.Incident.Status}}</span>
      <button id="theme-toggle" data-incident-id="{{.Incident.ID}}" title="Toggle dark mode">🌙</button>
    </div>
  </div>

  <div class="meta" id="incident-meta">
    <div class="meta-grid">
      <div class="meta-item"><label>ID</label><span><code>{{.Incident.ID}}</code></span></div>
      <div class="meta-item"><label>Service</label><span>{{.Incident.Service}}</span></div>
      <div class="meta-item"><label>Rule</label><span>{{.Incident.Rule}}</span></div>
      <div class="meta-item"><label>Metric</label><span>{{.Incident.Metric}}</span></div>
      <div class="meta-item"><label>Severity</label><span class="sev-{{.Incident.Severity}}">{{severityIcon .Incident.Severity}} {{.Incident.Severity}}</span></div>
      <div class="meta-item"><label>Created</label><span>{{.Incident.CreatedAt.Format "2006-01-02 15:04:05"}}</span></div>
    </div>

    {{if or .Incident.Forecast .Incident.Message}}
    <div class="ml-card">
      <h3>ML Analysis</h3>
      {{if .Incident.Forecast}}
      <div class="ml-grid">
        <div class="ml-item"><label>Forecast</label><span>{{printf "%.4f" .Incident.Forecast}}</span></div>
        <div class="ml-item"><label>Actual Value</label><span>{{printf "%.4f" .Incident.Value}}</span></div>
        <div class="ml-item"><label>Confidence Interval</label><span>[{{printf "%.2f" .Incident.LowerCI}}, {{printf "%.2f" .Incident.UpperCI}}]</span></div>
      </div>
      {{end}}
      {{if .Incident.Message}}
      <div class="ml-message">
        <label>Analysis</label>
        <p>{{.Incident.Message}}</p>
      </div>
      {{end}}
    </div>
    {{end}}

    {{if .Incident.ResolvedAt}}
    <div class="resolution">
      <label>Resolution ({{.Incident.ResolvedAt.Format "2006-01-02 15:04:05"}})</label>
      <p>{{.Incident.Resolution}}</p>
    </div>
    {{end}}
  </div>

  {{if gt (len .Incident.Events) 0}}
  <div class="chart-section">
    <h3>Signal Timeline ({{len .Incident.Events}} events)</h3>
    {{$chart := buildChart .Incident.Events}}
    {{if $chart}}
    {{$chart}}
    {{else}}
    <div class="no-chart">Not enough data for chart</div>
    {{end}}
  </div>
  {{end}}

  <div class="actions" id="incident-actions">
    {{if ne .Incident.Status "RESOLVED"}}
    <button class="escalate" id="btn-escalate" onclick="escalateIncident()">🔴 Escalate</button>
    <button class="resolve" id="btn-resolve" onclick="showResolve()">🟢 Resolve</button>
    {{end}}
  </div>

  <div id="resolve-form">
    <textarea id="resolution-text" placeholder="Describe the resolution (what was done, root cause, etc.)..."></textarea>
    <button class="confirm-resolve" onclick="resolveIncident()">Confirm Resolution</button>
  </div>

  <div class="section">
    <h2>Event Timeline</h2>
    <div class="timeline" id="event-timeline">
      {{range .Incident.Events}}
      <div class="event">
        <div class="event-time">{{.Timestamp.Format "2006-01-02 15:04:05"}}</div>
        {{$p := parsePayload .Payload}}
        {{if $p}}
        <div class="event-grid">
          {{if $p.Rule}}<div class="event-field"><label>Rule</label><span>{{$p.Rule}}</span></div>{{end}}
          {{if $p.Value}}<div class="event-field"><label>Value</label><span>{{printf "%.4f" $p.Value}}</span></div>{{end}}
          {{if $p.Forecast}}<div class="event-field"><label>Forecast</label><span>{{printf "%.4f" $p.Forecast}}</span></div>{{end}}
          {{if and $p.LowerCI $p.UpperCI}}<div class="event-field"><label>CI</label><span>[{{printf "%.2f" $p.LowerCI}}, {{printf "%.2f" $p.UpperCI}}]</span></div>{{end}}
          {{if $p.Severity}}<div class="event-field"><label>Severity</label><span class="sev-{{$p.Severity}}">{{severityIcon $p.Severity}} {{$p.Severity}}</span></div>{{end}}
          {{if $p.Message}}<div class="event-msg"><label>Message</label><span>{{$p.Message}}</span></div>{{end}}
        </div>
        {{else}}
        <pre style="font-size:12px;color:var(--text-secondary);overflow-x:auto">{{stringifyPayload .Payload}}</pre>
        {{end}}
      </div>
      {{else}}
      <p style="color:var(--text-secondary);font-size:14px">No events yet.</p>
      {{end}}
    </div>
  </div>

  <div class="section">
    <h2>Comments ({{len .Incident.Comments}})</h2>
    <div id="comments-list">
      {{range .Incident.Comments}}
      <div class="comment">
        <div class="comment-time">{{.CreatedAt.Format "2006-01-02 15:04:05"}}</div>
        <p>{{.Text}}</p>
      </div>
      {{else}}
      <p style="color:var(--text-secondary);font-size:14px" id="no-comments">No comments yet.</p>
      {{end}}
    </div>

    <div class="add-comment">
      <textarea id="comment-text" placeholder="Add a comment..."></textarea>
      <button class="submit-comment" onclick="addComment()">Add Comment</button>
    </div>
  </div>
</div>

<script>
(function(){
  var incidentID = document.getElementById('theme-toggle').dataset.incidentId;

  var theme = localStorage.getItem('theme') || 'light';
  document.documentElement.setAttribute('data-theme', theme);
  document.getElementById('theme-toggle').textContent = theme === 'dark' ? '☀️' : '🌙';
  document.getElementById('theme-toggle').onclick = function(){
    theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', theme);
    localStorage.setItem('theme', theme);
    this.textContent = theme === 'dark' ? '☀️' : '🌙';
  };

  function showToast(msg, type){
    var c = document.getElementById('toast-container');
    var d = document.createElement('div');
    d.className = 'toast toast-' + type;
    d.textContent = msg;
    c.appendChild(d);
    setTimeout(function(){ d.style.opacity='0'; setTimeout(function(){ d.remove(); }, 300); }, 3500);
  }
  window.showToast = showToast;

  function refreshIncident(){
    fetch('/api/v1/incidents/' + incidentID)
      .then(function(r){ return r.json(); })
      .then(function(data){
        var badge = document.querySelector('.status-badge');
        if(badge && data.status){
          var icons = {'OPEN':'🔵','UPDATED':'🟡','ESCALATED':'🔴','RESOLVED':'🟢'};
          badge.className = 'status-badge status-' + data.status;
          badge.innerHTML = (icons[data.status]||'⚪') + ' ' + data.status;
        }
        if(data.status === 'RESOLVED'){
          var actions = document.getElementById('incident-actions');
          if(actions) actions.innerHTML = '<span style="color:var(--text-secondary);font-size:13px">Incident resolved</span>';
          var rf = document.getElementById('resolve-form');
          if(rf) rf.style.display = 'none';
        }
        showToast('Updated', 'success');
      })
      .catch(function(){ showToast('Update failed', 'error'); });
  }

  window.showResolve = function(){
    document.getElementById('resolve-form').style.display = 'block';
  };

  window.escalateIncident = function(){
    var btn = document.getElementById('btn-escalate');
    btn.disabled = true;
    fetch('/api/v1/incidents/' + incidentID + '/escalate', {method:'POST'})
      .then(function(r){
        if(r.ok){
          showToast('Incident escalated', 'success');
          refreshIncident();
        } else {
          showToast('Failed to escalate', 'error');
          btn.disabled = false;
        }
      })
      .catch(function(){
        showToast('Failed to escalate', 'error');
        btn.disabled = false;
      });
  };

  window.resolveIncident = function(){
    var text = document.getElementById('resolution-text').value;
    if(!text.trim()){ showToast('Resolution text required', 'error'); return; }
    fetch('/api/v1/incidents/' + incidentID + '/resolve', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({resolution: text})
    })
      .then(function(r){
        if(r.ok){
          showToast('Incident resolved', 'success');
          document.getElementById('resolve-form').style.display = 'none';
          refreshIncident();
        } else {
          showToast('Failed to resolve', 'error');
        }
      })
      .catch(function(){ showToast('Failed to resolve', 'error'); });
  };

  window.addComment = function(){
    var text = document.getElementById('comment-text').value;
    if(!text.trim()){ showToast('Comment text required', 'error'); return; }
    fetch('/api/v1/incidents/' + incidentID + '/comment', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({text: text})
    })
      .then(function(r){
        if(r.ok){
          showToast('Comment added', 'success');
          r.json().then(function(c){
            var list = document.getElementById('comments-list');
            var noComments = document.getElementById('no-comments');
            if(noComments) noComments.remove();
            var div = document.createElement('div');
            div.className = 'comment';
            div.innerHTML = '<div class="comment-time">' + new Date().toLocaleString('en-CA',{year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'}).replace(',','') + '</div><p>' + escHtml(text) + '</p>';
            list.appendChild(div);
            document.getElementById('comment-text').value = '';
          });
        } else {
          showToast('Failed to add comment', 'error');
        }
      })
      .catch(function(){ showToast('Failed to add comment', 'error'); });
  };

  function escHtml(s){
    if(!s) return '';
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
  }
})();
</script>
</body>
</html>`))

// safeHTML marks a string as HTML-safe to prevent auto-escaping in templates.
func safeHTML(s string) template.HTML { return template.HTML(s) }
