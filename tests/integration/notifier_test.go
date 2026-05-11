package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestNotifierEndToEnd tests the full incident lifecycle:
// 1. Create incident via anomaly notification
// 2. Verify incident creation in database
// 3. Send another anomaly (update existing incident)
// 4. Escalate the incident
// 5. Add a comment
// 6. Resolve the incident
// 7. Verify all state changes in database
func TestNotifierEndToEnd(t *testing.T) {
	composeFile := filepath.Join(projectRoot(), "infra", "docker-compose.yml")

	// Start only timescaledb and notifier for this test.
	if out, err := dockerCompose("-f", composeFile, "up", "-d",
		"timescaledb", "notifier"); err != nil {
		t.Fatalf("docker compose up failed: %s\n%v", out, err)
	}
	t.Cleanup(func() {
		dockerCompose("-f", composeFile, "down", "-v")
	})

	// First, create the notifier database in timescaledb.
	dsn := "postgres://postgres:secret@localhost:5432/postgres?sslmode=disable"
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer dbCancel()

	for dbCtx.Err() == nil {
		conn, err := pgx.Connect(dbCtx, dsn)
		if err == nil {
			_, err = conn.Exec(dbCtx, "CREATE DATABASE notifier")
			if err != nil {
				// Database might already exist, which is fine
			}
			conn.Close(dbCtx)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if dbCtx.Err() != nil {
		t.Fatal("could not connect to postgres to create database")
	}

	// Wait for notifier HTTP server to be healthy.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	notifierURL := "http://localhost:8082"
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, notifierURL+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("notifier did not become healthy in time")
	}

	// ========== STEP 1: Create incident via anomaly notification ==========

	anomalyPayload := map[string]any{
		"rule":      "high_cpu",
		"service":   "auth-service",
		"metric":    "cpu_usage",
		"value":     0.92,
		"severity":  "critical",
		"message":   "CPU too high",
		"timestamp": time.Now().Unix(),
	}
	body, _ := json.Marshal(anomalyPayload)
	resp, err := http.Post(notifierURL+"/api/v1/notifications",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST notification failed: %v", err)
	}
	defer resp.Body.Close()

	// Read body before checking status
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 201 or 202, got %d: %s", resp.StatusCode, respBody)
	}

	// Parse response to get incident ID.
	var respData map[string]string
	json.Unmarshal(respBody, &respData)
	incidentID := respData["incident_id"]
	if incidentID == "" {
		t.Fatal("expected incident_id in response")
	}

	t.Logf("Created incident: %s", incidentID)

	// ========== STEP 2: Verify incident in database ==========

	verifyIncident := func(expectedStatus string, checkResolved bool) {
		incidentDsn := "postgres://postgres:secret@localhost:5432/notifier?sslmode=disable"
		conn, err := pgx.Connect(context.Background(), incidentDsn)
		if err != nil {
			t.Fatalf("pgx.Connect failed: %v", err)
		}
		defer conn.Close(context.Background())

		var rule, service, metric, status, severity string
		var resolvedAt *time.Time
		err = conn.QueryRow(context.Background(),
			"SELECT rule, service, metric, status, severity, resolved_at FROM incidents WHERE id = $1",
			incidentID).Scan(&rule, &service, &metric, &status, &severity, &resolvedAt)
		if err != nil {
			t.Fatalf("SELECT incident failed: %v", err)
		}

		if status != expectedStatus {
			t.Errorf("incident status = %q, want %q", status, expectedStatus)
		}
		if rule != "high_cpu" {
			t.Errorf("incident rule = %q, want high_cpu", rule)
		}
		if service != "auth-service" {
			t.Errorf("incident service = %q, want auth-service", service)
		}
		if checkResolved && resolvedAt == nil {
			t.Error("expected resolved_at to be set")
		}

		// Verify event was attached.
		var eventCount int
		err = conn.QueryRow(context.Background(),
			"SELECT COUNT(*) FROM incident_events WHERE incident_id = $1", incidentID).Scan(&eventCount)
		if err != nil {
			t.Fatalf("SELECT event count failed: %v", err)
		}
		if eventCount < 1 {
			t.Errorf("expected at least 1 event, got %d", eventCount)
		}
	}

	verifyIncident("OPEN", false)

	// ========== STEP 3: Send another anomaly (update existing incident) ==========

	anomalyPayload2 := map[string]any{
		"rule":      "high_cpu",
		"service":   "auth-service",
		"metric":    "cpu_usage",
		"value":     0.95,
		"severity":  "critical",
		"message":   "CPU even higher",
		"timestamp": time.Now().Unix(),
	}
	body2, _ := json.Marshal(anomalyPayload2)
	resp2, err := http.Post(notifierURL+"/api/v1/notifications",
		"application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatalf("POST second notification failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 201 for second notification, got %d: %s", resp2.StatusCode, b)
	}

	// Parse response - should be same incident ID.
	var respData2 map[string]string
	json.NewDecoder(resp2.Body).Decode(&respData2)
	incidentID2 := respData2["incident_id"]
	if incidentID != incidentID2 {
		t.Errorf("expected same incident ID on update, got %s and %s", incidentID, incidentID2)
	}

	// Verify incident was updated.
	verifyIncident("UPDATED", false)

	// Verify we now have 2 events.
	incidentDsn := "postgres://postgres:secret@localhost:5432/notifier?sslmode=disable"
	conn, _ := pgx.Connect(context.Background(), incidentDsn)
	var eventCount int
	conn.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM incident_events WHERE incident_id = $1", incidentID).Scan(&eventCount)
	conn.Close(context.Background())
	if eventCount != 2 {
		t.Errorf("expected 2 events after update, got %d", eventCount)
	}

	// ========== STEP 4: Escalate the incident ==========

	escalateReq, _ := http.NewRequestWithContext(context.Background(),
		http.MethodPost, notifierURL+"/api/v1/incidents/"+incidentID+"/escalate", nil)
	escalateResp, err := http.DefaultClient.Do(escalateReq)
	if err != nil {
		t.Fatalf("POST escalate failed: %v", err)
	}
	defer escalateResp.Body.Close()

	if escalateResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(escalateResp.Body)
		t.Fatalf("expected 200 for escalate, got %d: %s", escalateResp.StatusCode, b)
	}

	verifyIncident("ESCALATED", false)

	// ========== STEP 5: Add a comment ==========

	commentPayload := map[string]string{"text": "Investigating the high CPU usage"}
	commentBody, _ := json.Marshal(commentPayload)
	commentReq, _ := http.NewRequestWithContext(context.Background(),
		http.MethodPost, notifierURL+"/api/v1/incidents/"+incidentID+"/comment",
		bytes.NewReader(commentBody))
	commentReq.Header.Set("Content-Type", "application/json")
	commentResp, err := http.DefaultClient.Do(commentReq)
	if err != nil {
		t.Fatalf("POST comment failed: %v", err)
	}
	defer commentResp.Body.Close()

	if commentResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(commentResp.Body)
		t.Fatalf("expected 200 for comment, got %d: %s", commentResp.StatusCode, b)
	}

	// Verify comment in database.
	conn, _ = pgx.Connect(context.Background(), incidentDsn)
	var commentCount int
	conn.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM incident_comments WHERE incident_id = $1", incidentID).Scan(&commentCount)
	conn.Close(context.Background())
	if commentCount != 1 {
		t.Errorf("expected 1 comment, got %d", commentCount)
	}

	// ========== STEP 6: Resolve the incident ==========

	resolvePayload := map[string]string{"resolution": "Restarted the service and CPU is back to normal"}
	resolveBody, _ := json.Marshal(resolvePayload)
	resolveReq, _ := http.NewRequestWithContext(context.Background(),
		http.MethodPost, notifierURL+"/api/v1/incidents/"+incidentID+"/resolve",
		bytes.NewReader(resolveBody))
	resolveReq.Header.Set("Content-Type", "application/json")
	resolveResp, err := http.DefaultClient.Do(resolveReq)
	if err != nil {
		t.Fatalf("POST resolve failed: %v", err)
	}
	defer resolveResp.Body.Close()

	if resolveResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resolveResp.Body)
		t.Fatalf("expected 200 for resolve, got %d: %s", resolveResp.StatusCode, b)
	}

	verifyIncident("RESOLVED", true)

	// ========== STEP 7: Verify via GET /incidents/{id} API ==========

	getReq, _ := http.NewRequestWithContext(context.Background(),
		http.MethodGet, notifierURL+"/api/v1/incidents/"+incidentID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET incident failed: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(getResp.Body)
		t.Fatalf("expected 200 for GET, got %d: %s", getResp.StatusCode, b)
	}

	var incidentDetail map[string]any
	json.NewDecoder(getResp.Body).Decode(&incidentDetail)

	if incidentDetail["status"] != "RESOLVED" {
		t.Errorf("GET incident status = %q, want RESOLVED", incidentDetail["status"])
	}
	if incidentDetail["resolution"] != "Restarted the service and CPU is back to normal" {
		t.Errorf("GET incident resolution = %q, want expected resolution", incidentDetail["resolution"])
	}

	events, ok := incidentDetail["events"].([]any)
	if !ok || len(events) != 2 {
		t.Errorf("GET incident events count = %d, want 2", len(events))
	}

	comments, ok := incidentDetail["comments"].([]any)
	if !ok || len(comments) != 1 {
		t.Errorf("GET incident comments count = %d, want 1", len(comments))
	}

	// ========== STEP 8: Verify list API ==========

	listResp, err := http.Get(notifierURL + "/api/v1/incidents")
	if err != nil {
		t.Fatalf("GET incidents list failed: %v", err)
	}
	defer listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(listResp.Body)
		t.Fatalf("expected 200 for list, got %d: %s", listResp.StatusCode, b)
	}

	type incidentsResponse struct {
		Incidents []map[string]any `json:"incidents"`
		Page      int               `json:"page"`
		PageSize  int               `json:"page_size"`
		Total     int               `json:"total"`
	}

	var list incidentsResponse
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("failed to decode incidents response: %v", err)
	}
	if len(list.Incidents) < 1 {
		t.Error("expected at least 1 incident in list")
	}
	if list.Total < 1 {
		t.Error("expected total >= 1")
	}

	t.Log("Full incident lifecycle test passed!")
}

// TestNotifierDeduplication tests that duplicate anomalies with same rule+service+metric
// do not create new incidents.
func TestNotifierDeduplication(t *testing.T) {
	composeFile := filepath.Join(projectRoot(), "infra", "docker-compose.yml")

	if out, err := dockerCompose("-f", composeFile, "up", "-d",
		"timescaledb", "notifier"); err != nil {
		t.Fatalf("docker compose up failed: %s\n%v", out, err)
	}
	t.Cleanup(func() {
		dockerCompose("-f", composeFile, "down", "-v")
	})

	// Create database.
	dsn := "postgres://postgres:secret@localhost:5432/postgres?sslmode=disable"
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer dbCancel()

	for dbCtx.Err() == nil {
		conn, err := pgx.Connect(dbCtx, dsn)
		if err == nil {
			_, _ = conn.Exec(dbCtx, "CREATE DATABASE notifier")
			conn.Close(dbCtx)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Wait for notifier.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	notifierURL := "http://localhost:8082"
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, notifierURL+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("notifier did not become healthy")
	}

	// Send 3 anomalies with same dedup key.
	for i := range 3 {
		payload := map[string]any{
			"rule":      "high_memory",
			"service":   "api-service",
			"metric":    "memory_usage",
			"value":     0.85 + float64(i)*0.05,
			"severity":  "warning",
			"message":   fmt.Sprintf("Memory at %d%%", 85+i*5),
			"timestamp": time.Now().Unix(),
		}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(notifierURL+"/api/v1/notifications",
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST notification %d failed: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("notification %d: expected 201 or 202, got %d: %s", i+1, resp.StatusCode, b)
		}
	}

	// Verify only 1 incident exists in database.
	incidentDsn := "postgres://postgres:secret@localhost:5432/notifier?sslmode=disable"
	conn, err := pgx.Connect(context.Background(), incidentDsn)
	if err != nil {
		t.Fatalf("pgx.Connect failed: %v", err)
	}
	defer conn.Close(context.Background())

	var incidentCount int
	err = conn.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM incidents WHERE rule='high_memory'").Scan(&incidentCount)
	if err != nil {
		t.Fatalf("SELECT COUNT failed: %v", err)
	}

	if incidentCount != 1 {
		t.Errorf("expected 1 incident (dedup), got %d", incidentCount)
	}

	// Verify 3 events attached.
	var eventCount int
	err = conn.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM incident_events WHERE incident_id IN (SELECT id FROM incidents WHERE rule='high_memory')").Scan(&eventCount)
	if err != nil {
		t.Fatalf("SELECT event COUNT failed: %v", err)
	}

	if eventCount != 3 {
		t.Errorf("expected 3 events, got %d", eventCount)
	}

	t.Log("Deduplication test passed!")
}

// TestNotifierInvalidStatusTransitions tests that invalid status transitions are rejected.
func TestNotifierInvalidStatusTransitions(t *testing.T) {
	composeFile := filepath.Join(projectRoot(), "infra", "docker-compose.yml")

	if out, err := dockerCompose("-f", composeFile, "up", "-d",
		"timescaledb", "notifier"); err != nil {
		t.Fatalf("docker compose up failed: %s\n%v", out, err)
	}
	t.Cleanup(func() {
		dockerCompose("-f", composeFile, "down", "-v")
	})

	// Create database.
	dsn := "postgres://postgres:secret@localhost:5432/postgres?sslmode=disable"
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer dbCancel()

	for dbCtx.Err() == nil {
		conn, err := pgx.Connect(dbCtx, dsn)
		if err == nil {
			_, _ = conn.Exec(dbCtx, "CREATE DATABASE notifier")
			conn.Close(dbCtx)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Wait for notifier.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	notifierURL := "http://localhost:8082"
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, notifierURL+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("notifier did not become healthy")
	}

	// Create incident.
	payload := map[string]any{
		"rule":      "test_rule",
		"service":   "test_service",
		"metric":    "test_metric",
		"value":     0.5,
		"severity":  "info",
		"message":   "Test",
		"timestamp": time.Now().Unix(),
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(notifierURL+"/api/v1/notifications",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST notification failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 201 or 202, got %d: %s", resp.StatusCode, respBody)
	}

	var respData map[string]string
	json.Unmarshal(respBody, &respData)
	incidentID := respData["incident_id"]
	if incidentID == "" {
		t.Fatal("expected incident_id in response")
	}

	// Resolve the incident.
	resolvePayload := map[string]string{"resolution": "Fixed"}
	resolveBody, _ := json.Marshal(resolvePayload)
	resolveReq, _ := http.NewRequestWithContext(context.Background(),
		http.MethodPost, notifierURL+"/api/v1/incidents/"+incidentID+"/resolve",
		bytes.NewReader(resolveBody))
	resolveReq.Header.Set("Content-Type", "application/json")
	resolveResp, err := http.DefaultClient.Do(resolveReq)
	if err != nil {
		t.Fatalf("POST resolve failed: %v", err)
	}
	defer resolveResp.Body.Close()

	resolveRespBody, _ := io.ReadAll(resolveResp.Body)
	if resolveResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for resolve, got %d: %s", resolveResp.StatusCode, resolveRespBody)
	}

	// Try to escalate a resolved incident - should fail.
	escalateReq, _ := http.NewRequestWithContext(context.Background(),
		http.MethodPost, notifierURL+"/api/v1/incidents/"+incidentID+"/escalate", nil)
	escalateResp, err := http.DefaultClient.Do(escalateReq)
	if err != nil {
		t.Fatalf("POST escalate failed: %v", err)
	}
	escalateRespBody, _ := io.ReadAll(escalateResp.Body)
	escalateResp.Body.Close()

	if escalateResp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 for escalating resolved incident, got %d: %s", escalateResp.StatusCode, escalateRespBody)
	}

	// Try to resolve again - should fail.
	resolveReq2, _ := http.NewRequestWithContext(context.Background(),
		http.MethodPost, notifierURL+"/api/v1/incidents/"+incidentID+"/resolve",
		bytes.NewReader(resolveBody))
	resolveReq2.Header.Set("Content-Type", "application/json")
	resolveResp2, err := http.DefaultClient.Do(resolveReq2)
	if err != nil {
		t.Fatalf("POST resolve again failed: %v", err)
	}
	resolveResp2Body, _ := io.ReadAll(resolveResp2.Body)
	resolveResp2.Body.Close()

	if resolveResp2.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 for double resolve, got %d: %s", resolveResp2.StatusCode, resolveResp2Body)
	}

	t.Log("Invalid status transitions test passed!")
}
