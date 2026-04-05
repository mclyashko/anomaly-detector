package ui

import (
	"html/template"
	"log/slog"
	"net/http"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
)

// UI provides minimal web interface for incident management.
type UI struct {
	svc    *core.NotifierService
	logger *slog.Logger
}

// New creates a new UI handler.
func New(svc *core.NotifierService, logger *slog.Logger) *UI {
	return &UI{svc: svc, logger: logger}
}

// HandleList renders the incident list page.
func (u *UI) HandleList(w http.ResponseWriter, r *http.Request) {
	incidents, err := u.svc.ListIncidents(r.Context())
	if err != nil {
		u.logger.Error("failed to list incidents", "err", err)
		http.Error(w, "Internal Error", http.StatusInternalServerError)
		return
	}

	data := struct {
		Incidents []core.Incident
	}{Incidents: incidents}

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

var listTemplate = template.Must(template.New("list").Parse(`<!DOCTYPE html>
<html>
<head>
<title>Incidents</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; margin: 40px; }
h1 { color: #333; }
table { border-collapse: collapse; width: 100%; }
th, td { border: 1px solid #ddd; padding: 12px; text-align: left; }
th { background: #f5f5f5; }
.status { padding: 4px 8px; border-radius: 4px; font-size: 12px; }
.status-OPEN { background: #e3f2fd; color: #1565c0; }
.status-UPDATED { background: #fff3e0; color: #ef6c00; }
.status-ESCALATED { background: #ffebee; color: #c62828; }
.status-RESOLVED { background: #e8f5e9; color: #2e7d32; }
.severity-critical { color: #c62828; font-weight: bold; }
.severity-warning { color: #ef6c00; }
.severity-info { color: #1565c0; }
a { color: #1565c0; text-decoration: none; }
a:hover { text-decoration: underline; }
</style>
</head>
<body>
<h1>Incidents</h1>
<table>
<thead>
<tr>
<th>ID</th>
<th>Status</th>
<th>Service</th>
<th>Rule</th>
<th>Metric</th>
<th>Severity</th>
<th>Updated</th>
<th>Actions</th>
</tr>
</thead>
<tbody>
{{range .Incidents}}
<tr>
<td><code>{{.ID}}</code></td>
<td><span class="status status-{{.Status}}">{{.Status}}</span></td>
<td>{{.Service}}</td>
<td>{{.Rule}}</td>
<td>{{.Metric}}</td>
<td class="severity-{{.Severity}}">{{.Severity}}</td>
<td>{{.UpdatedAt.Format "2006-01-02 15:04:05"}}</td>
<td><a href="/incidents/{{.ID}}">View</a></td>
</tr>
{{else}}
<tr><td colspan="8">No incidents found</td></tr>
{{end}}
</tbody>
</table>
</body>
</html>`))

var detailTemplate = template.Must(template.New("detail").Parse(`<!DOCTYPE html>
<html>
<head>
<title>Incident {{.Incident.ID}}</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; margin: 40px; max-width: 900px; }
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
h1 { margin: 0; }
.status { padding: 6px 12px; border-radius: 4px; font-size: 14px; }
.status-OPEN { background: #e3f2fd; color: #1565c0; }
.status-UPDATED { background: #fff3e0; color: #ef6c00; }
.status-ESCALATED { background: #ffebee; color: #c62828; }
.status-RESOLVED { background: #e8f5e9; color: #2e7d32; }
.meta { background: #f5f5f5; padding: 20px; border-radius: 8px; margin-bottom: 20px; }
.meta-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 15px; }
.meta-item label { font-size: 12px; color: #666; display: block; }
.meta-item span { font-size: 16px; font-weight: 500; }
.severity-critical { color: #c62828; font-weight: bold; }
.severity-warning { color: #ef6c00; }
.severity-info { color: #1565c0; }
.section { margin-top: 30px; }
.section h2 { font-size: 18px; margin-bottom: 10px; }
.timeline { border-left: 2px solid #ddd; padding-left: 20px; margin-left: 10px; }
.event { margin-bottom: 15px; padding: 10px; background: #f9f9f9; border-radius: 4px; }
.event-time { font-size: 12px; color: #666; }
.event-payload { font-family: monospace; font-size: 13px; margin-top: 5px; white-space: pre-wrap; }
.comment { margin-bottom: 10px; padding: 10px; background: #f0f7ff; border-radius: 4px; }
.comment-time { font-size: 12px; color: #666; }
.actions { margin-top: 20px; display: flex; gap: 10px; }
button { padding: 10px 20px; border: none; border-radius: 4px; cursor: pointer; font-size: 14px; }
button.escalate { background: #ffebee; color: #c62828; }
button.resolve { background: #e8f5e9; color: #2e7d32; }
button:hover { opacity: 0.9; }
.resolution { background: #e8f5e9; padding: 15px; border-radius: 4px; margin-top: 15px; }
.resolution label { font-size: 12px; color: #666; }
.back { margin-bottom: 20px; }
.back a { color: #1565c0; text-decoration: none; }
</style>
</head>
<body>
<div class="back"><a href="/">&larr; Back to Incidents</a></div>

<div class="header">
<h1>Incident</h1>
<span class="status status-{{.Incident.Status}}">{{.Incident.Status}}</span>
</div>

<div class="meta">
<div class="meta-grid">
<div class="meta-item"><label>ID</label><span><code>{{.Incident.ID}}</code></span></div>
<div class="meta-item"><label>Service</label><span>{{.Incident.Service}}</span></div>
<div class="meta-item"><label>Rule</label><span>{{.Incident.Rule}}</span></div>
<div class="meta-item"><label>Metric</label><span>{{.Incident.Metric}}</span></div>
<div class="meta-item"><label>Severity</label><span class="severity-{{.Incident.Severity}}">{{.Incident.Severity}}</span></div>
<div class="meta-item"><label>Created</label><span>{{.Incident.CreatedAt.Format "2006-01-02 15:04:05"}}</span></div>
</div>
{{if .Incident.ResolvedAt}}
<div class="resolution">
<label>Resolution ({{.Incident.ResolvedAt.Format "2006-01-02 15:04:05"}})</label>
<p>{{.Incident.Resolution}}</p>
</div>
{{end}}
</div>

<div class="actions">
{{if ne .Incident.Status "RESOLVED"}}
<button class="escalate" onclick="escalate()">Escalate</button>
<button class="resolve" onclick="showResolve()">Resolve</button>
{{end}}
</div>

<div id="resolve-form" style="display:none; margin-top:15px;">
<textarea id="resolution-text" rows="3" style="width:100%;padding:10px;" placeholder="Resolution description..."></textarea>
<button class="resolve" onclick="resolve()">Confirm Resolution</button>
</div>

<div class="section">
<h2>Event Timeline ({{len .Incident.Events}} events)</h2>
<div class="timeline">
{{range .Incident.Events}}
<div class="event">
<div class="event-time">{{.Timestamp.Format "2006-01-02 15:04:05"}}</div>
<div class="event-payload">{{.Payload}}</div>
</div>
{{else}}
<p>No events yet</p>
{{end}}
</div>
</div>

<div class="section">
<h2>Comments ({{len .Incident.Comments}})</h2>
{{range .Incident.Comments}}
<div class="comment">
<div class="comment-time">{{.CreatedAt.Format "2006-01-02 15:04:05"}}</div>
<p>{{.Text}}</p>
</div>
{{else}}
<p>No comments yet</p>
{{end}}

<h3>Add Comment</h3>
<textarea id="comment-text" rows="3" style="width:100%;padding:10px;" placeholder="Add a comment..."></textarea>
<button onclick="addComment()" style="margin-top:10px;padding:10px 20px;background:#1565c0;color:white;border:none;border-radius:4px;cursor:pointer;">Submit</button>
</div>

<script>
const incidentID = "{{.Incident.ID}}";

async function escalate() {
  await fetch('/api/v1/incidents/' + incidentID + '/escalate', { method: 'POST' });
  location.reload();
}

function showResolve() {
  document.getElementById('resolve-form').style.display = 'block';
}

async function resolve() {
  const text = document.getElementById('resolution-text').value;
  await fetch('/api/v1/incidents/' + incidentID + '/resolve', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ resolution: text })
  });
  location.reload();
}

async function addComment() {
  const text = document.getElementById('comment-text').value;
  if (!text.trim()) return;
  await fetch('/api/v1/incidents/' + incidentID + '/comment', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text })
  });
  location.reload();
}
</script>
</body>
</html>`))
