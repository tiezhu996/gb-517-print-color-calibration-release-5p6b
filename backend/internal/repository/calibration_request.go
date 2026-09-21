package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/model"
	"gorm.io/gorm"
)

const (
	calibrationPending = "pending"
	calibrationPassed  = "passed"
	calibrationFailed  = "failed"
)

var (
	ErrDuplicatePending = errors.New("batch already has a pending calibration request")
	ErrCodeConflict     = errors.New("calibration request code already exists")
	// ErrAlreadySettled means a retest was backfilled; a second attempt must
	// not touch the stored evidence.
	ErrAlreadySettled = errors.New("calibration request was already settled")
)

// CalibrationRepository owns persistence for the 批次色彩复校准 closed loop,
// including cross-aggregate transactions for run state and quarantine.
type CalibrationRepository interface {
	List(context.Context, dto.CalibrationPageQuery) (Page[model.CalibrationRequest], error)
	Get(context.Context, uint) (model.CalibrationRequest, error)
	FindPendingForRun(context.Context, uint) (model.CalibrationRequest, error)
	LatestForRuns(context.Context, []uint) (map[uint]model.CalibrationRequest, error)
	Insert(context.Context, *model.CalibrationRequest) error
	CompletePassed(context.Context, uint, uint, float64, string, string, time.Time) error
	CompleteFailed(context.Context, *model.CalibrationRequest, *model.PrintRun, *model.ReleaseDecision, string, string, string, time.Time) error
}

type calibrationRepository struct{ db *gorm.DB }

func NewCalibrationRepository(db *gorm.DB) CalibrationRepository {
	return &calibrationRepository{db: db}
}

func (r *calibrationRepository) List(ctx context.Context, q dto.CalibrationPageQuery) (Page[model.CalibrationRequest], error) {
	page, pageSize := normalizePage(q.Page, q.PageSize)
	db := r.db.WithContext(ctx).Model(&model.CalibrationRequest{})
	if status := strings.TrimSpace(q.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	if q.PrintRunID != 0 {
		db = db.Where("print_run_id = ?", q.PrintRunID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return Page[model.CalibrationRequest]{}, err
	}
	items := make([]model.CalibrationRequest, 0)
	if err := db.Order("updated_at DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return Page[model.CalibrationRequest]{}, err
	}
	attachDeviations(items)
	attachRunContext(ctx, r.db, items)
	return Page[model.CalibrationRequest]{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (r *calibrationRepository) Get(ctx context.Context, id uint) (model.CalibrationRequest, error) {
	var item model.CalibrationRequest
	if err := r.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return item, err
	}
	if item.MeasuredDelta != nil {
		deviation := *item.MeasuredDelta - item.TargetDelta
		item.Deviation = &deviation
	}
	if item.QuarantineDecisionID != nil {
		var decision model.ReleaseDecision
		if err := r.db.WithContext(ctx).Select("id", "code").First(&decision, *item.QuarantineDecisionID).Error; err == nil {
			item.QuarantineCode = decision.Code
		}
	}
	return item, nil
}

func (r *calibrationRepository) FindPendingForRun(ctx context.Context, runID uint) (model.CalibrationRequest, error) {
	var item model.CalibrationRequest
	err := r.db.WithContext(ctx).Where("print_run_id = ? AND status = ?", runID, calibrationPending).First(&item).Error
	return item, err
}

// LatestForRuns returns the newest calibration request per run in one query.
func (r *calibrationRepository) LatestForRuns(ctx context.Context, runIDs []uint) (map[uint]model.CalibrationRequest, error) {
	result := make(map[uint]model.CalibrationRequest)
	if len(runIDs) == 0 {
		return result, nil
	}
	items := make([]model.CalibrationRequest, 0)
	err := r.db.WithContext(ctx).
		Raw(`SELECT c.* FROM calibration_requests c
			INNER JOIN (SELECT print_run_id AS run_id, MAX(id) AS max_id FROM calibration_requests WHERE print_run_id IN ? GROUP BY print_run_id) latest
			ON c.id = latest.max_id`, runIDs).Scan(&items).Error
	if err != nil {
		return nil, err
	}
	attachDeviations(items)
	for _, item := range items {
		result[item.PrintRunID] = item
	}
	return result, nil
}

// attachRunContext fills run status and linked quarantine codes for list rows.
func attachRunContext(ctx context.Context, db *gorm.DB, items []model.CalibrationRequest) {
	if len(items) == 0 {
		return
	}
	runIDs, decisionIDs := make([]uint, 0, len(items)), make([]uint, 0, len(items))
	seenRun, seenDecision := map[uint]bool{}, map[uint]bool{}
	for _, item := range items {
		if !seenRun[item.PrintRunID] {
			seenRun[item.PrintRunID] = true
			runIDs = append(runIDs, item.PrintRunID)
		}
		if item.QuarantineDecisionID != nil && !seenDecision[*item.QuarantineDecisionID] {
			seenDecision[*item.QuarantineDecisionID] = true
			decisionIDs = append(decisionIDs, *item.QuarantineDecisionID)
		}
	}
	statusByID := map[uint]string{}
	var runs []model.PrintRun
	if err := db.WithContext(ctx).Select("id", "status").Where("id IN ?", runIDs).Find(&runs).Error; err == nil {
		for _, run := range runs {
			statusByID[run.ID] = run.Status
		}
	}
	codeByDecision := map[uint]string{}
	if len(decisionIDs) > 0 {
		var decisions []model.ReleaseDecision
		if err := db.WithContext(ctx).Select("id", "code").Where("id IN ?", decisionIDs).Find(&decisions).Error; err == nil {
			for _, decision := range decisions {
				codeByDecision[decision.ID] = decision.Code
			}
		}
	}
	for i := range items {
		items[i].RunStatus = statusByID[items[i].PrintRunID]
		if items[i].QuarantineDecisionID != nil {
			items[i].QuarantineCode = codeByDecision[*items[i].QuarantineDecisionID]
		}
	}
}

func (r *calibrationRepository) Insert(ctx context.Context, item *model.CalibrationRequest) error {
	err := r.db.WithContext(ctx).Create(item).Error
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueViolation(err) {
		// Distinguish the two unique constraints via the business invariant.
		var existing int64
		lookupErr := r.db.WithContext(ctx).Model(&model.CalibrationRequest{}).
			Where("print_run_id = ? AND status = ?", item.PrintRunID, calibrationPending).
			Count(&existing).Error
		if lookupErr == nil && existing > 0 {
			return ErrDuplicatePending
		}
		if strings.Contains(strings.ToLower(err.Error()), "pending_run_id") && existing == 0 {
			return ErrDuplicatePending
		}
		return ErrCodeConflict
	}
	return err
}

// isUniqueViolation recognises driver-specific unique-constraint errors
// because the SQLite dev driver lacks gorm duplicate-key translation.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") ||
		strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "duplicate key value") ||
		strings.Contains(message, "error 1062")
}

func settleCalibrationMap(status string, measured *float64, note, actor string, now time.Time) map[string]any {
	return map[string]any{
		"status": status, "version": gorm.Expr("version + 1"),
		"measured_delta": measured, "result_note": note,
		"completed_at": now.UTC(), "completed_by": actor, "updated_at": now.UTC(),
		"pending_run_id": nil,
	}
}

// CompletePassed backfills evidence only while pending and the version
// matches; the batch stays in proofing and the release gate is cleared.
func (r *calibrationRepository) CompletePassed(ctx context.Context, id, expectedVersion uint, measured float64, note, actor string, now time.Time) error {
	measuredRef := measured
	result := r.db.WithContext(ctx).Model(&model.CalibrationRequest{}).
		Where("id = ? AND version = ? AND status = ?", id, expectedVersion, calibrationPending).
		Updates(settleCalibrationMap(calibrationPassed, &measuredRef, note, actor, now))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return r.classifySettled(ctx, id)
	}
	return nil
}

// CompleteFailed performs the full out-of-tolerance reaction in one
// transaction: settle once, hold the batch, append both immutable revisions
// and create the quarantine decision. Any failure rolls everything back.
func (r *calibrationRepository) CompleteFailed(ctx context.Context, calibration *model.CalibrationRequest, run *model.PrintRun, decision *model.ReleaseDecision, note, actor, requestID string, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		settled := tx.Model(&model.CalibrationRequest{}).
			Where("id = ? AND version = ? AND status = ?", calibration.ID, calibration.Version, calibrationPending).
			Updates(settleCalibrationMap(calibrationFailed, calibration.MeasuredDelta, note, actor, now))
		if settled.Error != nil {
			return settled.Error
		}
		if settled.RowsAffected == 0 {
			return ErrAlreadySettled
		}
		runResult := tx.Model(&model.PrintRun{}).
			Where("id = ? AND version = ? AND status = ?", run.ID, run.Version, "proofing").
			Updates(map[string]any{"status": "hold", "version": gorm.Expr("version + 1"), "updated_at": now.UTC()})
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected == 0 {
			return ErrVersionConflict
		}
		if err := tx.Omit("Revisions").Create(decision).Error; err != nil {
			return err
		}
		holdSnapshot := *run
		holdSnapshot.ID, holdSnapshot.Status, holdSnapshot.Version = run.ID, "hold", run.Version+1
		if err := tx.Create(printRunRevision(&holdSnapshot, actor, requestID, "colour recalibration exceeded target delta; batch held")).Error; err != nil {
			return err
		}
		if err := tx.Create(releaseDecisionRevision(decision, actor, requestID, "auto quarantine after recalibration out of tolerance")).Error; err != nil {
			return err
		}
		// Persist the decision link so refreshes and list views read it back.
		return tx.Model(&model.CalibrationRequest{}).Where("id = ?", calibration.ID).
			Update("quarantine_decision_id", decision.ID).Error
	})
}

// classifySettled distinguishes a concurrent settlement from a stale version.
func (r *calibrationRepository) classifySettled(ctx context.Context, id uint) error {
	var current model.CalibrationRequest
	if err := r.db.WithContext(ctx).Select("status").First(&current, id).Error; err != nil {
		return err
	}
	if current.Status != calibrationPending {
		return ErrAlreadySettled
	}
	return ErrVersionConflict
}

func attachDeviations(items []model.CalibrationRequest) {
	for i := range items {
		if items[i].MeasuredDelta != nil {
			deviation := *items[i].MeasuredDelta - items[i].TargetDelta
			items[i].Deviation = &deviation
		}
	}
}
