package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/blueship581/print-color-calibration-release/backend/internal/database"
	"github.com/blueship581/print-color-calibration-release/backend/internal/router"
	"github.com/gin-gonic/gin"
)

// TestCalibrationClosedLoop covers: reviewer-only scheduling, one pending per
// run, equipment/deadline rejection without state change, duplicate resolution
// succeeding once, pass releasing the gate, fail moving the run to hold with a
// generated quarantine decision, and the immutable evidence chain.
func TestCalibrationClosedLoop(t *testing.T) {
	cfg := testConfig(filepath.Join(t.TempDir(), "gb517-cal.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, redisClient, err := database.Open(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	engine := router.New(cfg, db, redisClient, logger)

	tokens := map[string]string{}
	for _, role := range []string{"viewer", "operator", "reviewer", "admin"} {
		tokens[role] = loginToken(t, engine, role)
	}

	// operator creates a run and moves it to proofing.
	payload := recordPayload("PR-CAL-001", "复校准测试批次")
	status, body := perform(t, engine, http.MethodPost, "/api/runs", tokens["operator"], "cal-run-create", payload)
	run := decodeData[struct {
		ID      uint   `json:"id"`
		Version uint   `json:"version"`
		Status  string `json:"status"`
	}](t, body)
	if status != http.StatusCreated {
		t.Fatalf("create run status = %d body=%s", status, body)
	}
	runPath := "/api/runs/" + uintString(run.ID) + "/transition"
	for _, target := range []string{"printing", "proofing"} {
		if status, _ = perform(t, engine, http.MethodPost, runPath, tokens["operator"], "cal-run-"+target,
			map[string]any{"status": target, "expectedVersion": run.Version, "reason": "advance to " + target}); status != http.StatusOK {
			t.Fatalf("run -> %s status = %d", target, status)
		}
		run.Version++
	}

	// pick the seeded ready press PU-001 for scheduling.
	pressID := pressIDByCode(t, engine, tokens["reviewer"], "PU-001")
	// move PU-002 (setup) into maintenance to verify equipment rejection.
	maintenanceID := pressIDByCode(t, engine, tokens["reviewer"], "PU-002")
	pressPath := "/api/presses/" + uintString(maintenanceID) + "/transition"
	if status, _ := perform(t, engine, http.MethodPost, pressPath, tokens["operator"], "cal-press-maintenance-set",
		map[string]any{"status": "maintenance", "expectedVersion": 1, "reason": "scheduled maintenance window"}); status != http.StatusOK {
		t.Fatalf("press -> maintenance status = %d", status)
	}

	schedule := func(overrides map[string]any, requestID string) (int, []byte) {
		input := map[string]any{
			"printRunId": run.ID, "pressId": pressID, "targetDelta": 2.0,
			"sample": "首件签样和随机抽样", "retestDueAt": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
			"reason": "proof colour drift requires recalibration",
		}
		for key, value := range overrides {
			input[key] = value
		}
		return perform(t, engine, http.MethodPost, "/api/calibrations", tokens["reviewer"], requestID, input)
	}

	// operator/viewer cannot schedule.
	if status, _ := perform(t, engine, http.MethodPost, "/api/calibrations", tokens["operator"], "operator-cal", scheduleBody(run.ID, pressID)); status != http.StatusForbidden {
		t.Fatalf("operator schedule status = %d, want 403", status)
	}
	// maintenance equipment is rejected.
	if status, _ := schedule(map[string]any{"pressId": maintenanceID}, "cal-press-maintenance"); status != http.StatusUnprocessableEntity {
		t.Fatalf("maintenance press schedule status = %d, want 422", status)
	}
	// past due date is rejected.
	if status, _ := schedule(map[string]any{"retestDueAt": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}, "cal-bad-due"); status != http.StatusUnprocessableEntity {
		t.Fatalf("past due schedule status = %d, want 422", status)
	}
	// run must remain proofing with its version unchanged.
	_, body = perform(t, engine, http.MethodGet, "/api/runs/"+uintString(run.ID), tokens["reviewer"], "cal-run-check", nil)
	checked := decodeData[struct {
		Status  string `json:"status"`
		Version uint   `json:"version"`
	}](t, body)
	if checked.Status != "proofing" || checked.Version != run.Version {
		t.Fatalf("run state changed after rejected calibration: %+v", checked)
	}

	// reviewer schedules successfully.
	status, body = schedule(nil, "cal-schedule")
	if status != http.StatusCreated {
		t.Fatalf("schedule status = %d body=%s", status, body)
	}
	calibration := decodeData[struct {
		ID      uint   `json:"id"`
		Version uint   `json:"version"`
		Status  string `json:"status"`
	}](t, body)
	// duplicate pending request is rejected.
	if status, _ := schedule(nil, "cal-duplicate"); status != http.StatusConflict {
		t.Fatalf("duplicate schedule status = %d, want 409", status)
	}

	// while pending, run transitions (incl. release) are blocked.
	if status, _ := perform(t, engine, http.MethodPost, runPath, tokens["reviewer"], "cal-release-blocked",
		map[string]any{"status": "released", "expectedVersion": run.Version, "reason": "try release while calibrating"}); status != http.StatusConflict {
		t.Fatalf("release while pending status = %d, want 409", status)
	}

	resolve := func(token, requestID string, measured float64, version uint) (int, []byte) {
		return perform(t, engine, http.MethodPost, "/api/calibrations/"+uintString(calibration.ID)+"/resolve", token, requestID,
			map[string]any{"expectedVersion": version, "measuredDelta": measured, "evidence": "spectrophotometer retest", "reason": "retest completed"})
	}

	// operator cannot resolve.
	if status, _ := resolve(tokens["operator"], "operator-resolve", 1.2, calibration.Version); status != http.StatusForbidden {
		t.Fatalf("operator resolve status = %d, want 403", status)
	}
	// first completion passes (measured <= target).
	status, body = resolve(tokens["reviewer"], "cal-pass", 1.5, calibration.Version)
	if status != http.StatusOK {
		t.Fatalf("resolve pass status = %d body=%s", status, body)
	}
	passed := decodeData[struct {
		Status    string  `json:"status"`
		Result    string  `json:"result"`
		Measured  float64 `json:"measuredDelta"`
		Version   uint    `json:"version"`
		Revisions []struct {
			Version uint `json:"version"`
		} `json:"revisions"`
	}](t, body)
	if passed.Status != "passed" || passed.Result != "passed" || passed.Version != 2 || len(passed.Revisions) != 2 {
		t.Fatalf("unexpected passed calibration: %+v", passed)
	}
	// repeat resolution cannot overwrite evidence.
	if status, _ := resolve(tokens["reviewer"], "cal-pass-again", 9.9, passed.Version); status != http.StatusConflict {
		t.Fatalf("duplicate resolve status = %d, want 409", status)
	}
	_, body = perform(t, engine, http.MethodGet, "/api/calibrations/"+uintString(calibration.ID), tokens["reviewer"], "cal-evidence-read", nil)
	evidence := decodeData[struct {
		Measured *float64 `json:"measuredDelta"`
		Result   string   `json:"result"`
	}](t, body)
	if evidence.Measured == nil || *evidence.Measured != 1.5 || evidence.Result != "passed" {
		t.Fatalf("evidence was overwritten: %+v", evidence)
	}

	// second run exercises the over-tolerance branch.
	payload2 := recordPayload("PR-CAL-002", "超差隔离测试批次")
	status, body = perform(t, engine, http.MethodPost, "/api/runs", tokens["operator"], "cal-run2-create", payload2)
	run2 := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, body)
	if status != http.StatusCreated {
		t.Fatalf("create run2 status = %d body=%s", status, body)
	}
	run2Path := "/api/runs/" + uintString(run2.ID) + "/transition"
	for _, target := range []string{"printing", "proofing"} {
		if status, _ = perform(t, engine, http.MethodPost, run2Path, tokens["operator"], "cal-run2-"+target,
			map[string]any{"status": target, "expectedVersion": run2.Version, "reason": "advance to " + target}); status != http.StatusOK {
			t.Fatalf("run2 -> %s status = %d", target, status)
		}
		run2.Version++
	}
	status, body = perform(t, engine, http.MethodPost, "/api/calibrations", tokens["reviewer"], "cal2-schedule", scheduleBody(run2.ID, pressID))
	if status != http.StatusCreated {
		t.Fatalf("schedule run2 status = %d body=%s", status, body)
	}
	calibration2 := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, body)
	// over-tolerance completion fails, holds the run and creates a quarantine.
	status, body = perform(t, engine, http.MethodPost, "/api/calibrations/"+uintString(calibration2.ID)+"/resolve", tokens["reviewer"], "cal2-fail",
		map[string]any{"expectedVersion": calibration2.Version, "measuredDelta": 3.8, "evidence": "retest over tolerance", "reason": "retest over tolerance"})
	if status != http.StatusOK {
		t.Fatalf("resolve fail status = %d body=%s", status, body)
	}
	failed := decodeData[struct {
		Status         string `json:"status"`
		QuarantineCode string `json:"quarantineCode"`
	}](t, body)
	if failed.Status != "failed" || failed.QuarantineCode == "" {
		t.Fatalf("failed calibration missing quarantine link: %+v", failed)
	}
	_, body = perform(t, engine, http.MethodGet, "/api/runs/"+uintString(run2.ID), tokens["reviewer"], "cal2-run-held", nil)
	held := decodeData[struct {
		Status  string `json:"status"`
		Version uint   `json:"version"`
	}](t, body)
	if held.Status != "hold" || held.Version != run2.Version+1 {
		t.Fatalf("run not held after failed calibration: %+v", held)
	}
	// failed calibration blocks release even after the run returns to proofing.
	if status, _ = perform(t, engine, http.MethodPost, run2Path, tokens["reviewer"], "cal2-back-proofing",
		map[string]any{"status": "proofing", "expectedVersion": held.Version, "reason": "rework done, re-proof"}); status != http.StatusOK {
		t.Fatalf("hold -> proofing status = %d", status)
	}
	if status, _ = perform(t, engine, http.MethodPost, run2Path, tokens["reviewer"], "cal2-release-blocked",
		map[string]any{"status": "released", "expectedVersion": held.Version + 1, "reason": "try release after fail"}); status != http.StatusConflict {
		t.Fatalf("release after failed calibration status = %d, want 409", status)
	}
	// the generated decision is terminal quarantine and immutable.
	_, body = perform(t, engine, http.MethodGet, "/api/release", tokens["reviewer"], "cal2-decision-list", nil)
	// simple GET by listing already covered; verify via detail code through list data.
	list := decodeList[struct {
		Code   string `json:"code"`
		Status string `json:"status"`
	}](t, body)
	found := false
	for _, decision := range list {
		if decision.Code == failed.QuarantineCode && decision.Status == "quarantine" {
			found = true
		}
	}
	if !found {
		t.Fatalf("quarantine decision %s not present in release list: %+v", failed.QuarantineCode, list)
	}
}

func scheduleBody(runID, pressID uint) map[string]any {
	return map[string]any{
		"printRunId": runID, "pressId": pressID, "targetDelta": 2.0,
		"sample": "首件签样和随机抽样", "retestDueAt": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		"reason": "proof colour drift requires recalibration",
	}
}

func pressIDByCode(t *testing.T, engine *gin.Engine, token, code string) uint {
	t.Helper()
	status, body := perform(t, engine, http.MethodGet, "/api/presses?page=1&pageSize=100&search="+code, token, "press-lookup-"+code, nil)
	if status != http.StatusOK {
		t.Fatalf("list presses status = %d body=%s", status, body)
	}
	items := decodeList[struct {
		ID     uint   `json:"id"`
		Code   string `json:"code"`
		Status string `json:"status"`
	}](t, body)
	for _, press := range items {
		if press.Code == code {
			return press.ID
		}
	}
	t.Fatalf("press %s not found in %+v", code, items)
	return 0
}

func decodeList[T any](t *testing.T, body []byte) []T {
	t.Helper()
	var envelope struct {
		Data []T `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode list %s: %v", body, err)
	}
	return envelope.Data
}

// TestCalibrationConcurrentScheduleAndResolve proves that concurrent requests
// can occupy the pending slot and resolve it at most once.
func TestCalibrationConcurrentScheduleAndResolve(t *testing.T) {
	cfg := testConfig(filepath.Join(t.TempDir(), "gb517-cal-concurrent.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, redisClient, err := database.Open(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	engine := router.New(cfg, db, redisClient, logger)
	operator := loginToken(t, engine, "operator")
	reviewer := loginToken(t, engine, "reviewer")

	status, body := perform(t, engine, http.MethodPost, "/api/runs", operator, "cc-run-create", recordPayload("PR-CONC-001", "并发复校准批次"))
	run := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, body)
	if status != http.StatusCreated {
		t.Fatalf("create run status = %d body=%s", status, body)
	}
	runPath := "/api/runs/" + uintString(run.ID) + "/transition"
	for _, target := range []string{"printing", "proofing"} {
		if status, _ = perform(t, engine, http.MethodPost, runPath, operator, "cc-run-"+target,
			map[string]any{"status": target, "expectedVersion": run.Version, "reason": "advance to " + target}); status != http.StatusOK {
			t.Fatalf("run -> %s status = %d", target, status)
		}
		run.Version++
	}
	pressID := pressIDByCode(t, engine, reviewer, "PU-001")

	const clients = 8
	type outcome struct{ status int }
	scheduleCh := make(chan outcome, clients)
	for i := 0; i < clients; i++ {
		go func(i int) {
			code := postStatus(engine, http.MethodPost, "/api/calibrations", reviewer, "cc-schedule",
				scheduleBody(run.ID, pressID))
			scheduleCh <- outcome{code}
		}(i)
	}
	created, conflict := 0, 0
	for i := 0; i < clients; i++ {
		switch result := <-scheduleCh; result.status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("unexpected schedule status %d", result.status)
		}
	}
	if created != 1 || conflict != clients-1 {
		t.Fatalf("schedule concurrency created=%d conflict=%d, want exactly one created", created, conflict)
	}

	// identify the single pending calibration.
	_, body = perform(t, engine, http.MethodGet, "/api/calibrations?printRunId="+uintString(run.ID), reviewer, "cc-list", nil)
	pending := decodeList[struct {
		ID      uint   `json:"id"`
		Version uint   `json:"version"`
		Status  string `json:"status"`
	}](t, body)
	if len(pending) != 1 || pending[0].Status != "pending" {
		t.Fatalf("unexpected pending list: %+v", pending)
	}

	resolveCh := make(chan outcome, clients)
	for i := 0; i < clients; i++ {
		go func(i int) {
			s := postStatus(engine, http.MethodPost, "/api/calibrations/"+uintString(pending[0].ID)+"/resolve", reviewer, "cc-resolve",
				map[string]any{"expectedVersion": pending[0].Version, "measuredDelta": 1.1, "evidence": "concurrent retest", "reason": "concurrent resolution"})
			resolveCh <- outcome{s}
		}(i)
	}
	resolved, blocked := 0, 0
	for i := 0; i < clients; i++ {
		switch result := <-resolveCh; result.status {
		case http.StatusOK:
			resolved++
		case http.StatusConflict:
			blocked++
		default:
			t.Fatalf("unexpected resolve status %d", result.status)
		}
	}
	if resolved != 1 || blocked != clients-1 {
		t.Fatalf("resolve concurrency resolved=%d blocked=%d, want exactly one resolved", resolved, blocked)
	}
	_, body = perform(t, engine, http.MethodGet, "/api/calibrations/"+uintString(pending[0].ID), reviewer, "cc-final", nil)
	final := decodeData[struct {
		Status     string  `json:"status"`
		Result     string  `json:"result"`
		Measured   float64 `json:"measuredDelta"`
		Quarantine string  `json:"quarantineCode"`
	}](t, body)
	if final.Status != "passed" || final.Result != "passed" || final.Measured != 1.1 || final.Quarantine != "" {
		t.Fatalf("unexpected final calibration state: %+v", final)
	}
}

// postStatus performs a JSON request without touching the test state, so it is
// safe to call from concurrent goroutines.
func postStatus(engine *gin.Engine, method, path, token, requestID string, payload any) int {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return http.StatusInternalServerError
		}
		body = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("X-Request-ID", requestID)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response.Code
}
