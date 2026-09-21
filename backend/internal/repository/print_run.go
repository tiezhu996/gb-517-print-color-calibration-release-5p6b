package repository

import (
	"context"

	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/model"
	"gorm.io/gorm"
)

// PrintRunRepository owns all persistence operations for 印刷批次.
type PrintRunRepository interface {
	List(context.Context, dto.PageQuery) (Page[model.PrintRun], error)
	Get(context.Context, uint) (model.PrintRun, error)
	CreateVersioned(context.Context, *model.PrintRun, string, string, string) error
	UpdateVersioned(context.Context, uint, uint, *model.PrintRun, string, string, string) error
	Delete(context.Context, uint) error
	CountByStatus(context.Context) (map[string]int64, error)
}

type printRunRepository struct {
	store       *Store[model.PrintRun]
	calibration CalibrationRepository
}

func NewPrintRunRepository(db *gorm.DB, calibration CalibrationRepository) PrintRunRepository {
	return &printRunRepository{store: NewStore[model.PrintRun](db), calibration: calibration}
}

func (r *printRunRepository) List(ctx context.Context, q dto.PageQuery) (Page[model.PrintRun], error) {
	page, err := r.store.List(ctx, q)
	if err != nil {
		return page, err
	}
	runIDs := make([]uint, 0, len(page.Items))
	for _, item := range page.Items {
		runIDs = append(runIDs, item.ID)
	}
	latest, err := r.calibration.LatestForRuns(ctx, runIDs)
	if err != nil {
		return page, err
	}
	for i := range page.Items {
		if calibration, ok := latest[page.Items[i].ID]; ok {
			calibrationCopy := calibration
			page.Items[i].LatestCalibration = &calibrationCopy
		}
	}
	return page, nil
}
func (r *printRunRepository) Get(ctx context.Context, id uint) (model.PrintRun, error) {
	var item model.PrintRun
	err := r.store.db.WithContext(ctx).
		Preload("Revisions", func(db *gorm.DB) *gorm.DB { return db.Order("version DESC") }).
		Preload("Calibrations", func(db *gorm.DB) *gorm.DB { return db.Order("id DESC") }).
		First(&item, id).Error
	if err != nil {
		return item, err
	}
	if len(item.Calibrations) > 0 {
		latest := item.Calibrations[0]
		item.LatestCalibration = &latest
	}
	return item, nil
}
func (r *printRunRepository) CreateVersioned(ctx context.Context, item *model.PrintRun, actor, requestID, reason string) error {
	return r.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Omit("Revisions", "Calibrations").Create(item).Error; err != nil {
			return err
		}
		return tx.Create(printRunRevision(item, actor, requestID, reason)).Error
	})
}
func (r *printRunRepository) UpdateVersioned(ctx context.Context, id, version uint, item *model.PrintRun, actor, requestID, reason string) error {
	return r.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PrintRun{}).Where("id = ? AND version = ?", id, version).
			Select("*").Omit("id", "code", "created_at", "deleted_at", "Revisions", "Calibrations", "LatestCalibration").Updates(item)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrVersionConflict
		}
		return tx.Create(printRunRevision(item, actor, requestID, reason)).Error
	})
}

func printRunRevision(item *model.PrintRun, actor, requestID, reason string) *model.PrintRunRevision {
	return &model.PrintRunRevision{
		PrintRunID: item.ID, Version: item.Version, Status: item.Status, Name: item.Name,
		Facility: item.Facility, Owner: item.Owner, Category: item.Category,
		RiskLevel: item.RiskLevel, MetricValue: item.MetricValue, MetricUnit: item.MetricUnit,
		Evidence: item.Evidence, RelatedCode: item.RelatedCode,
		Actor: actor, RequestID: requestID, Reason: reason,
	}
}
func (r *printRunRepository) Delete(ctx context.Context, id uint) error {
	return r.store.Delete(ctx, id)
}
func (r *printRunRepository) CountByStatus(ctx context.Context) (map[string]int64, error) {
	return r.store.CountByStatus(ctx)
}
