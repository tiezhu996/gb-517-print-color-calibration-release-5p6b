package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/blueship581/print-color-calibration-release/backend/internal/database"
	"github.com/blueship581/print-color-calibration-release/backend/internal/router"
	"github.com/gin-gonic/gin"
)

// TestCalibrationClosedLoop exercises the batch colour re-calibration loop
// end to end: reviewer-only registration, the single-pending constraint,
// equipment/deadline rejection without state changes, the release gate,
// one-shot retest backfill (pass and fail), quarantine generation and
// repeat/competing completion that must never overwrite evidence.
func TestCalibrationClosedLoop(t *testing.T) {
	engine := bootCalibrationEngine(t)
	tokens := map[string]string{}
	for _, role := range []string{"viewer", "operator", "reviewer", "admin"} {
		tokens[role] = loginToken(t, engine, role)
	}

	proofingRunID, proofingVersion := runToProofing(t, engine, tokens["operator"], "PR-CAL-FLOW")
	future := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)

	// Writers other than reviewer are rejected.
	createBody := map[string]any{
		"printRunId": proofingRunID, "pressUnitId": 1, "targetDelta": 2.0,
		"samples": "青/品红/黄 三色偏色条", "retestDueAt": future,
	}
	if status, _ := perform(t, engine, http.MethodPost, "/api/calibrations", tokens["viewer"], "cal-viewer", createBody); status != http.StatusForbidden {
		t.Fatalf("viewer calibration create = %d, want 403", status)
	}
	if status, _ := perform(t, engine, http.MethodPost, "/api/calibrations", tokens["operator"], "cal-operator", createBody); status != http.StatusForbidden {
		t.Fatalf("operator calibration create = %d, want 403", status)
	}

	// A past deadline is rejected and changes nothing.
	pastBody := cloneBody(createBody)
	pastBody["retestDueAt"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if status, body := perform(t, engine, http.MethodPost, "/api/calibrations", tokens["reviewer"], "cal-past-due", pastBody); status != http.StatusUnprocessableEntity {
		t.Fatalf("past deadline create = %d body=%s, want 422", status, body)
	}

	// Equipment in maintenance is unavailable and rejected without side effects.
	transitionPress(t, engine, tokens["operator"], 3, "maintenance")
	unavailableBody := cloneBody(createBody)
	unavailableBody["pressUnitId"] = 3
	if status, body := perform(t, engine, http.MethodPost, "/api/calibrations", tokens["reviewer"], "cal-press-down", unavailableBody); status != http.StatusUnprocessableEntity {
		t.Fatalf("maintenance press create = %d body=%s, want 422", status, body)
	}

	// Reviewer registration succeeds.
	status, body := perform(t, engine, http.MethodPost, "/api/calibrations", tokens["reviewer"], "cal-open", createBody)
	if status != http.StatusCreated {
		t.Fatalf("calibration create = %d body=%s, want 201", status, body)
	}
	calibration := decodeData[struct {
		ID          uint    `json:"id"`
		Version     uint    `json:"version"`
		Status      string  `json:"status"`
		TargetDelta float64 `json:"targetDelta"`
		PressCode   string  `json:"pressCode"`
		PrintRunID  uint    `json:"printRunId"`
	}](t, body)
	if calibration.Status != "pending" || calibration.Version != 1 || calibration.TargetDelta != 2.0 || calibration.PressCode != "PU-001" {
		t.Fatalf("unexpected calibration: %+v", calibration)
	}

	// The run now embeds its latest calibration request.
	_, runBody := perform(t, engine, http.MethodGet, fmt.Sprintf("/api/runs/%d", proofingRunID), tokens["reviewer"], "cal-run-read", nil)
	runDetail := decodeData[struct {
		LatestCalibration *struct {
			ID uint `json:"id"`
		} `json:"latestCalibration"`
		Calibrations []struct {
			ID uint `json:"id"`
		} `json:"calibrations"`
	}](t, runBody)
	if runDetail.LatestCalibration == nil || runDetail.LatestCalibration.ID != calibration.ID || len(runDetail.Calibrations) != 1 {
		t.Fatalf("run should embed the open calibration: %+v", runDetail)
	}

	// A second registration for the same batch is a conflict; the original
	// pending request keeps its identity.
	if status, _ := perform(t, engine, http.MethodPost, "/api/calibrations", tokens["admin"], "cal-duplicate", createBody); status != http.StatusConflict {
		t.Fatalf("duplicate pending create = %d, want 409", status)
	}

	// Release is blocked while the calibration is open.
	blocked := map[string]any{"status": "released", "expectedVersion": proofingVersion, "reason": "attempt release before retest"}
	if status, _ := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/runs/%d/transition", proofingRunID), tokens["reviewer"], "cal-block-release", blocked); status != http.StatusUnprocessableEntity {
		t.Fatalf("release with open calibration = %d, want 422", status)
	}

	// A result that contradicts the measured-vs-target derivation is rejected.
	mismatch := map[string]any{"expectedVersion": calibration.Version, "measuredDelta": 1.2, "result": "failed", "resultNote": "should be pass"}
	if status, _ := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/calibrations/%d/complete", calibration.ID), tokens["reviewer"], "cal-mismatch", mismatch); status != http.StatusUnprocessableEntity {
		t.Fatalf("contradictory completion = %d, want 422", status)
	}
	// Operator can never backfill a retest.
	if status, _ := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/calibrations/%d/complete", calibration.ID), tokens["operator"], "cal-op-complete", mismatch); status != http.StatusForbidden {
		t.Fatalf("operator complete = %d, want 403", status)
	}

	// Passed retest settles the request, keeps the batch in proofing and opens
	// the release gate.
	pass := map[string]any{"expectedVersion": calibration.Version, "measuredDelta": 1.6, "result": "passed", "resultNote": "within tolerance"}
	status, body = perform(t, engine, http.MethodPost, fmt.Sprintf("/api/calibrations/%d/complete", calibration.ID), tokens["reviewer"], "cal-pass", pass)
	if status != http.StatusOK {
		t.Fatalf("passed completion = %d body=%s, want 200", status, body)
	}
	passed := decodeData[struct {
		Status    string   `json:"status"`
		Version   uint     `json:"version"`
		Deviation *float64 `json:"deviation"`
		RunStatus string   `json:"runStatus"`
	}](t, body)
	if passed.Status != "passed" || passed.Version != 2 || passed.Deviation == nil || math.Abs(*passed.Deviation+0.4) > 1e-9 || passed.RunStatus != "proofing" {
		t.Fatalf("unexpected passed payload: %+v", passed)
	}
	// Repeated completion is rejected and cannot overwrite the evidence.
	if status, _ := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/calibrations/%d/complete", calibration.ID), tokens["reviewer"], "cal-pass-again", pass); status != http.StatusConflict {
		t.Fatalf("repeat completion = %d, want 409", status)
	}
	_, body = perform(t, engine, http.MethodGet, fmt.Sprintf("/api/calibrations/%d", calibration.ID), tokens["reviewer"], "cal-read-after", nil)
	after := decodeData[struct {
		Status        string   `json:"status"`
		MeasuredDelta *float64 `json:"measuredDelta"`
		CompletedBy   string   `json:"completedBy"`
	}](t, body)
	if after.Status != "passed" || after.MeasuredDelta == nil || *after.MeasuredDelta != 1.6 || after.CompletedBy != "reviewer" {
		t.Fatalf("evidence changed after rejected repeat: %+v", after)
	}
	// Release is now allowed.
	release := map[string]any{"status": "released", "expectedVersion": proofingVersion, "reason": "retest passed; release unblocked"}
	if status, body := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/runs/%d/transition", proofingRunID), tokens["reviewer"], "cal-release", release); status != http.StatusOK {
		t.Fatalf("release after passed retest = %d body=%s, want 200", status, body)
	}

	// Failure flow on a fresh proofing batch: out-of-tolerance backfill holds
	// the batch and generates a quarantine decision exactly once.
	failRunID, failRunVersion := runToProofing(t, engine, tokens["operator"], "PR-CAL-FAIL")
	failCreate := map[string]any{
		"printRunId": failRunID, "pressUnitId": 2, "targetDelta": 2.0,
		"samples": "四色偏色条与灰平衡", "retestDueAt": future,
	}
	status, body = perform(t, engine, http.MethodPost, "/api/calibrations", tokens["reviewer"], "cal-fail-open", failCreate)
	if status != http.StatusCreated {
		t.Fatalf("second calibration create = %d body=%s", status, body)
	}
	failCalibration := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, body)
	failPayload := map[string]any{"expectedVersion": failCalibration.Version, "measuredDelta": 3.4, "result": "failed", "resultNote": "magenta channel out of tolerance"}
	status, body = perform(t, engine, http.MethodPost, fmt.Sprintf("/api/calibrations/%d/complete", failCalibration.ID), tokens["reviewer"], "cal-fail", failPayload)
	if status != http.StatusOK {
		t.Fatalf("failed completion = %d body=%s, want 200", status, body)
	}
	failed := decodeData[struct {
		Status               string  `json:"status"`
		Deviation            float64 `json:"deviation"`
		RunStatus            string  `json:"runStatus"`
		QuarantineCode       string  `json:"quarantineCode"`
		QuarantineDecisionID uint    `json:"quarantineDecisionId"`
	}](t, body)
	if failed.Status != "failed" || math.Abs(failed.Deviation-1.4) > 1e-9 || failed.RunStatus != "hold" || failed.QuarantineCode == "" || failed.QuarantineDecisionID == 0 {
		t.Fatalf("unexpected failed payload: %+v", failed)
	}
	// Batch is held with a bumped optimistic version.
	_, body = perform(t, engine, http.MethodGet, fmt.Sprintf("/api/runs/%d", failRunID), tokens["reviewer"], "cal-fail-run", nil)
	heldRun := decodeData[struct {
		Status  string `json:"status"`
		Version uint   `json:"version"`
	}](t, body)
	if heldRun.Status != "hold" || heldRun.Version != failRunVersion+1 {
		t.Fatalf("run after failed retest = %+v, want hold v%d", heldRun, failRunVersion+1)
	}
	// The generated quarantine decision exists, is immutable and carries the
	// measurement evidence.
	_, body = perform(t, engine, http.MethodGet, fmt.Sprintf("/api/release/%d", failed.QuarantineDecisionID), tokens["reviewer"], "cal-quarantine-read", nil)
	quarantine := decodeData[struct {
		Status      string  `json:"status"`
		MetricValue float64 `json:"metricValue"`
		Revisions   []struct {
			Version uint `json:"version"`
		} `json:"revisions"`
	}](t, body)
	if quarantine.Status != "quarantine" || quarantine.MetricValue != 3.4 || len(quarantine.Revisions) != 1 {
		t.Fatalf("unexpected quarantine decision: %+v", quarantine)
	}
	// A stale/duplicate failure completion must fail and leave evidence intact.
	staleFail := map[string]any{"expectedVersion": failCalibration.Version, "measuredDelta": 1.0, "result": "passed", "resultNote": "attempted overwrite"}
	if status, _ := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/calibrations/%d/complete", failCalibration.ID), tokens["reviewer"], "cal-fail-again", staleFail); status != http.StatusConflict {
		t.Fatalf("duplicate failed completion = %d, want 409", status)
	}
	_, body = perform(t, engine, http.MethodGet, fmt.Sprintf("/api/calibrations/%d", failCalibration.ID), tokens["reviewer"], "cal-fail-evidence", nil)
	preserved := decodeData[struct {
		Status        string   `json:"status"`
		MeasuredDelta *float64 `json:"measuredDelta"`
	}](t, body)
	if preserved.Status != "failed" || preserved.MeasuredDelta == nil || *preserved.MeasuredDelta != 3.4 {
		t.Fatalf("failure evidence was overwritten: %+v", preserved)
	}

	// The calibration list supports filtering and round-trips after refresh.
	status, body = perform(t, engine, http.MethodGet, "/api/calibrations?status=pending&page=1&pageSize=20", tokens["viewer"], "cal-list-pending", nil)
	if status != http.StatusOK {
		t.Fatalf("calibration list = %d", status)
	}
	list := decodeList[struct {
		Status string `json:"status"`
	}](t, body)
	if len(list) == 0 {
		t.Fatal("expected seed pending calibration in filtered list")
	}
	for _, item := range list {
		if item.Status != "pending" {
			t.Fatalf("pending filter returned %s", item.Status)
		}
	}
}

func bootCalibrationEngine(t *testing.T) *gin.Engine {
	t.Helper()
	cfg := testConfig(filepath.Join(t.TempDir(), "gb517-calibration.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, redisClient, err := database.Open(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	// Serialize SQLite access so concurrent HTTP requests exercise the
	// conditional-update guards instead of tripping SQLite write locks.
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	return router.New(cfg, db, redisClient, logger)
}

// TestCalibrationConcurrentRaces proves the database-level guards make
// duplicate and parallel requests succeed exactly once.
func TestCalibrationConcurrentRaces(t *testing.T) {
	engine := bootCalibrationEngine(t)
	token := loginToken(t, engine, "reviewer")
	operator := loginToken(t, engine, "operator")

	runID, _ := runToProofing(t, engine, operator, "PR-CAL-RACE")
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	body := map[string]any{
		"printRunId": runID, "pressUnitId": 1, "targetDelta": 2.0,
		"samples": "并发复校准样本", "retestDueAt": future,
	}

	const writers = 12
	createStatuses := make(chan int, writers)
	for i := 0; i < writers; i++ {
		go func() {
			status, _ := performStatus(engine, http.MethodPost, "/api/calibrations", token, "cal-race-create", body)
			createStatuses <- status
		}()
	}
	created, conflicts := 0, 0
	for i := 0; i < writers; i++ {
		switch <-createStatuses {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected parallel create status")
		}
	}
	if created != 1 || conflicts != writers-1 {
		t.Fatalf("parallel create: %d created, %d conflicts, want exactly 1 created", created, conflicts)
	}

	_, listBody := perform(t, engine, http.MethodGet, fmt.Sprintf("/api/calibrations?printRunId=%d", runID), token, "cal-race-list", nil)
	calibrations := decodeList[struct {
		ID      uint   `json:"id"`
		Version uint   `json:"version"`
		Status  string `json:"status"`
	}](t, listBody)
	if len(calibrations) != 1 || calibrations[0].Status != "pending" {
		t.Fatalf("expected exactly one pending calibration, got %+v", calibrations)
	}
	calibrationID, version := calibrations[0].ID, calibrations[0].Version

	completeBody := map[string]any{"expectedVersion": version, "measuredDelta": 1.1, "result": "passed", "resultNote": "parallel completion race"}
	completeStatuses := make(chan int, writers)
	for i := 0; i < writers; i++ {
		go func() {
			status, _ := performStatus(engine, http.MethodPost, fmt.Sprintf("/api/calibrations/%d/complete", calibrationID), token, "cal-race-complete", completeBody)
			completeStatuses <- status
		}()
	}
	okCount, rejected := 0, 0
	for i := 0; i < writers; i++ {
		switch <-completeStatuses {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
			rejected++
		default:
			t.Fatal("unexpected parallel complete status")
		}
	}
	if okCount != 1 || rejected != writers-1 {
		t.Fatalf("parallel complete: %d ok, %d rejected, want exactly 1 ok", okCount, rejected)
	}
	_, detailBody := perform(t, engine, http.MethodGet, fmt.Sprintf("/api/calibrations/%d", calibrationID), token, "cal-race-detail", nil)
	final := decodeData[struct {
		Status        string   `json:"status"`
		Version       uint     `json:"version"`
		MeasuredDelta *float64 `json:"measuredDelta"`
	}](t, detailBody)
	if final.Status != "passed" || final.Version != 2 || final.MeasuredDelta == nil || *final.MeasuredDelta != 1.1 {
		t.Fatalf("race settlement corrupted evidence: %+v", final)
	}
}

func cloneBody(body map[string]any) map[string]any {
	clone := make(map[string]any, len(body))
	for key, value := range body {
		clone[key] = value
	}
	return clone
}

func runToProofing(t *testing.T, engine *gin.Engine, token, code string) (uint, uint) {
	t.Helper()
	payload := recordPayload(code, "校准闭环测试批次")
	status, body := perform(t, engine, http.MethodPost, "/api/runs", token, "cal-run-create", payload)
	if status != http.StatusCreated {
		t.Fatalf("create run = %d body=%s", status, body)
	}
	run := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, body)
	for _, next := range []string{"printing", "proofing"} {
		transition := map[string]any{"status": next, "expectedVersion": run.Version, "reason": "calibration loop test progression"}
		status, body = perform(t, engine, http.MethodPost, fmt.Sprintf("/api/runs/%d/transition", run.ID), token, "cal-run-"+next, transition)
		if status != http.StatusOK {
			t.Fatalf("run -> %s = %d body=%s", next, status, body)
		}
		run.Version++
	}
	return run.ID, run.Version
}

func transitionPress(t *testing.T, engine *gin.Engine, token string, id uint, target string) uint {
	t.Helper()
	_, body := perform(t, engine, http.MethodGet, fmt.Sprintf("/api/presses/%d", id), token, "cal-press-read", nil)
	press := decodeData[struct {
		Version uint `json:"version"`
	}](t, body)
	transition := map[string]any{"status": target, "expectedVersion": press.Version, "reason": "schedule maintenance for calibration test"}
	status, body := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/presses/%d/transition", id), token, "cal-press-maint", transition)
	if status != http.StatusOK {
		t.Fatalf("press -> %s = %d body=%s", target, status, body)
	}
	return press.Version + 1
}

func decodeList[T any](t *testing.T, body []byte) []T {
	t.Helper()
	var envelope struct {
		Data []T `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode list envelope %s: %v", body, err)
	}
	return envelope.Data
}

// performStatus mirrors perform without failing the test, so goroutines in
// the concurrency scenario can report HTTP statuses safely.
func performStatus(engine *gin.Engine, method, path, token, requestID string, payload any) (int, []byte) {
	var bodyReader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return http.StatusInternalServerError, nil
		}
		bodyReader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, bodyReader)
	request.Header.Set("X-Request-ID", requestID)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response.Code, response.Body.Bytes()
}
