package repository

import (
	"context"
	"errors"
	"strconv"

	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/model"
	"gorm.io/gorm"
)

// ErrDuplicatePending is returned when the unique pending slot for a print run
// is already occupied, including under concurrent inserts.
var ErrDuplicatePending = errors.New("print run already has a pending calibration request")

// CalibrationRequestRepository owns persistence for 批次色彩复校准申请. The
// closed-loop methods accept a transaction so run/decision updates commit
// atomically with the calibration evidence.
type CalibrationRequestRepository interface {
	List(context.Context, dto.CalibrationQuery) (Page[model.CalibrationRequest], error)
	Get(context.Context, uint) (model.CalibrationRequest, error)
	Transaction(context.Context, func(tx *gorm.DB) error) error
	InsertTx(tx *gorm.DB, item *model.CalibrationRequest, actor, requestID, reason string) error
	ResolveTx(tx *gorm.DB, id, expectedVersion uint, item *model.CalibrationRequest, actor, requestID, reason string) error
	LatestForRun(ctx context.Context, runID uint) (model.CalibrationRequest, error)
	LatestForRunTx(tx *gorm.DB, runID uint) (model.CalibrationRequest, error)
	UpdateRunVersionedTx(tx *gorm.DB, id, expectedVersion uint, item *model.PrintRun, actor, requestID, reason string) error
	CreateQuarantineDecisionTx(tx *gorm.DB, item *model.ReleaseDecision, actor, requestID, reason string) error
}

type calibrationRequestRepository struct {
	db *gorm.DB
}

func NewCalibrationRequestRepository(db *gorm.DB) CalibrationRequestRepository {
	return &calibrationRequestRepository{db: db}
}

func (r *calibrationRequestRepository) List(ctx context.Context, q dto.CalibrationQuery) (Page[model.CalibrationRequest], error) {
	page, pageSize := normalizePage(q.Page, q.PageSize)
	db := r.db.WithContext(ctx).Model(&model.CalibrationRequest{})
	if search := q.Search; search != "" {
		wildcard := "%" + search + "%"
		db = db.Where("code LIKE ? OR sample LIKE ? OR print_run_code LIKE ? OR press_code LIKE ?", wildcard, wildcard, wildcard, wildcard)
	}
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	if q.PrintRunID != 0 {
		db = db.Where("print_run_id = ?", q.PrintRunID)
	}
	if q.RunCode != "" {
		db = db.Where("print_run_code = ?", q.RunCode)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return Page[model.CalibrationRequest]{}, err
	}
	items := make([]model.CalibrationRequest, 0)
	err := db.Order("updated_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error
	return Page[model.CalibrationRequest]{Items: items, Total: total, Page: page, PageSize: pageSize}, err
}

func (r *calibrationRequestRepository) Get(ctx context.Context, id uint) (model.CalibrationRequest, error) {
	var item model.CalibrationRequest
	err := r.db.WithContext(ctx).
		Preload("Revisions", func(db *gorm.DB) *gorm.DB { return db.Order("version DESC") }).
		First(&item, id).Error
	return item, err
}

func (r *calibrationRequestRepository) Transaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(fn)
}

func (r *calibrationRequestRepository) InsertTx(tx *gorm.DB, item *model.CalibrationRequest, actor, requestID, reason string) error {
	slot := calibrationPendingSlot(item.PrintRunID)
	item.PendingSlot = &slot
	if err := tx.Omit("Revisions").Create(item).Error; err != nil {
		if isDuplicateKey(err) {
			return ErrDuplicatePending
		}
		return err
	}
	return tx.Create(calibrationRevision(item, actor, requestID, reason)).Error
}

func (r *calibrationRequestRepository) ResolveTx(tx *gorm.DB, id, expectedVersion uint, item *model.CalibrationRequest, actor, requestID, reason string) error {
	// Only a pending row at the expected version can be resolved. Clearing the
	// pending slot releases the run for a later calibration cycle.
	item.PendingSlot = nil
	result := tx.Model(&model.CalibrationRequest{}).
		Where("id = ? AND version = ? AND status = ?", id, expectedVersion, model.CalibrationRequestInitialStatus).
		Select("*").Omit("id", "code", "created_at", "deleted_at", "Revisions").Updates(item)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrVersionConflict
	}
	return tx.Create(calibrationRevision(item, actor, requestID, reason)).Error
}

func (r *calibrationRequestRepository) LatestForRun(ctx context.Context, runID uint) (model.CalibrationRequest, error) {
	return r.LatestForRunTx(r.db.WithContext(ctx), runID)
}

func (r *calibrationRequestRepository) LatestForRunTx(tx *gorm.DB, runID uint) (model.CalibrationRequest, error) {
	var item model.CalibrationRequest
	err := tx.Where("print_run_id = ?", runID).Order("id DESC").First(&item).Error
	return item, err
}

func (r *calibrationRequestRepository) UpdateRunVersionedTx(tx *gorm.DB, id, expectedVersion uint, item *model.PrintRun, actor, requestID, reason string) error {
	result := tx.Model(&model.PrintRun{}).Where("id = ? AND version = ?", id, expectedVersion).
		Select("*").Omit("id", "code", "created_at", "deleted_at", "Revisions").Updates(item)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrVersionConflict
	}
	return tx.Create(printRunRevision(item, actor, requestID, reason)).Error
}

func (r *calibrationRequestRepository) CreateQuarantineDecisionTx(tx *gorm.DB, item *model.ReleaseDecision, actor, requestID, reason string) error {
	if err := tx.Omit("Revisions").Create(item).Error; err != nil {
		return err
	}
	return tx.Create(releaseDecisionRevision(item, actor, requestID, reason)).Error
}

func calibrationRevision(item *model.CalibrationRequest, actor, requestID, reason string) *model.CalibrationRevision {
	return &model.CalibrationRevision{
		CalibrationRequestID: item.ID, Version: item.Version, Status: item.Status,
		TargetDelta: item.TargetDelta, Sample: item.Sample, RetestDueAt: item.RetestDueAt.UTC(),
		MeasuredDelta: item.MeasuredDelta, Result: item.Result, Evidence: item.Evidence,
		PressCode: item.PressCode, PrintRunCode: item.PrintRunCode, QuarantineCode: item.QuarantineCode,
		Actor: actor, RequestID: requestID, Reason: reason,
	}
}

func calibrationPendingSlot(runID uint) string {
	return "run-" + strconv.FormatUint(uint64(runID), 10)
}
