package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/blueship581/print-color-calibration-release/backend/internal/constants"
	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/model"
	"github.com/blueship581/print-color-calibration-release/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CalibrationService implements the batch colour re-calibration closed loop.
// A reviewer schedules one pending request, retest results resolve it exactly
// once, and a failed retest moves the batch to hold with a quarantine decision.
type CalibrationService interface {
	List(context.Context, dto.CalibrationQuery) (repository.Page[model.CalibrationRequest], error)
	Get(context.Context, uint) (model.CalibrationRequest, error)
	Create(context.Context, dto.CreateCalibrationRequest, string, string) (model.CalibrationRequest, error)
	Resolve(context.Context, uint, dto.CompleteCalibrationRequest, string, string) (model.CalibrationRequest, error)
}

type calibrationService struct {
	repository repository.CalibrationRequestRepository
	runs       repository.PrintRunRepository
	presses    repository.PressUnitRepository
	decisions  repository.ReleaseDecisionRepository
	security   SecurityService
}

func NewCalibrationService(repo repository.CalibrationRequestRepository, runs repository.PrintRunRepository,
	presses repository.PressUnitRepository, decisions repository.ReleaseDecisionRepository, security SecurityService) CalibrationService {
	return &calibrationService{repository: repo, runs: runs, presses: presses, decisions: decisions, security: security}
}

func (s *calibrationService) List(ctx context.Context, query dto.CalibrationQuery) (repository.Page[model.CalibrationRequest], error) {
	return s.repository.List(ctx, query)
}

func (s *calibrationService) Get(ctx context.Context, id uint) (model.CalibrationRequest, error) {
	return s.repository.Get(ctx, id)
}

func (s *calibrationService) Create(ctx context.Context, input dto.CreateCalibrationRequest, actor, requestID string) (model.CalibrationRequest, error) {
	if input.PrintRunID == 0 || input.PressID == 0 || input.TargetDelta <= 0 || math.IsNaN(input.TargetDelta) {
		return model.CalibrationRequest{}, ErrInvalidInput
	}
	now := time.Now().UTC()
	if !input.RetestDueAt.UTC().After(now) {
		return model.CalibrationRequest{}, ErrInvalidDueDate
	}
	run, err := s.runs.Get(ctx, input.PrintRunID)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	if run.Status != string(constants.RunStateProofing) {
		return model.CalibrationRequest{}, ErrRunNotProofing
	}
	press, err := s.presses.Get(ctx, input.PressID)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	if press.Status == "maintenance" {
		return model.CalibrationRequest{}, ErrPressUnavailable
	}
	// Fast rejection of duplicates outside the transaction; the unique pending
	// slot remains the authoritative guard under concurrency.
	if latest, err := s.repository.LatestForRun(ctx, run.ID); err == nil {
		if latest.Status == model.CalibrationRequestInitialStatus {
			return model.CalibrationRequest{}, ErrDuplicatePending
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CalibrationRequest{}, err
	}

	code := strings.TrimSpace(input.Code)
	if code == "" {
		code = "CR-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	}
	item := model.CalibrationRequest{
		BaseModel: model.BaseModel{
			Code:        strings.ToUpper(code),
			Name:        "色彩复校准 · " + run.Code,
			Status:      model.CalibrationRequestInitialStatus,
			Version:     1,
			Description: fmt.Sprintf("复核人 %s 对批次 %s 的色彩复校准申请", actor, run.Code),
		},
		PrintRunID:   run.ID,
		PrintRunCode: run.Code,
		PressID:      press.ID,
		PressCode:    press.Code,
		TargetDelta:  input.TargetDelta,
		Sample:       strings.TrimSpace(input.Sample),
		RetestDueAt:  input.RetestDueAt.UTC(),
		Evidence:     strings.TrimSpace(input.Evidence),
	}
	reason := input.Reason
	if err := s.repository.Transaction(ctx, func(tx *gorm.DB) error {
		return s.repository.InsertTx(tx, &item, actor, requestID, reason)
	}); err != nil {
		if errors.Is(err, repository.ErrDuplicatePending) {
			return model.CalibrationRequest{}, ErrDuplicatePending
		}
		return model.CalibrationRequest{}, fmt.Errorf("create 色彩复校准申请: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "calibrate", "CalibrationRequest", item.ID, "", item.Status,
		fmt.Sprintf("scheduled calibration for run %s on press %s, target ΔE %.2f, due %s", run.Code, press.Code, item.TargetDelta, item.RetestDueAt.Format(time.RFC3339)))
	return s.repository.Get(ctx, item.ID)
}

func (s *calibrationService) Resolve(ctx context.Context, id uint, input dto.CompleteCalibrationRequest, actor, requestID string) (model.CalibrationRequest, error) {
	if input.MeasuredDelta < 0 || math.IsNaN(input.MeasuredDelta) {
		return model.CalibrationRequest{}, ErrInvalidInput
	}
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	if current.Status != model.CalibrationRequestInitialStatus {
		// A resolved request is immutable evidence: retest retries cannot
		// overwrite the recorded measurement.
		return model.CalibrationRequest{}, ErrLocked
	}
	run, err := s.runs.Get(ctx, current.PrintRunID)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	if run.Status != string(constants.RunStateProofing) {
		return model.CalibrationRequest{}, ErrRunNotProofing
	}

	passed := input.MeasuredDelta <= current.TargetDelta
	result := string(constants.CalibrationStatusFailed)
	if passed {
		result = string(constants.CalibrationStatusPassed)
	}
	resolvedAt := time.Now().UTC()
	current.Status = result
	current.Version = input.ExpectedVersion + 1
	current.MeasuredDelta = &input.MeasuredDelta
	current.Result = result
	if evidence := strings.TrimSpace(input.Evidence); evidence != "" {
		current.Evidence = evidence
	}
	current.ResolvedBy = actor
	current.ResolvedAt = &resolvedAt
	current.UpdatedAt = resolvedAt

	var heldRun model.PrintRun
	var quarantine *model.ReleaseDecision
	reason := input.Reason
	if !passed {
		quarantine = s.quarantineDecision(run, current, actor)
		current.QuarantineCode = quarantine.Code
	}
	txErr := s.repository.Transaction(ctx, func(tx *gorm.DB) error {
		if err := s.repository.ResolveTx(tx, id, input.ExpectedVersion, &current, actor, requestID, reason); err != nil {
			return err
		}
		if passed {
			return nil
		}
		// Over-tolerance: the batch moves to hold and a quarantine decision is
		// generated in the same transaction so release can never slip through.
		heldRun = run
		heldRun.Status = string(constants.RunStateHold)
		heldRun.Version = run.Version + 1
		heldRun.UpdatedAt = resolvedAt
		holdReason := fmt.Sprintf("calibration %s exceeded target ΔE %.2f (measured %.2f); batch held", current.Code, current.TargetDelta, input.MeasuredDelta)
		if err := s.repository.UpdateRunVersionedTx(tx, run.ID, run.Version, &heldRun, actor, requestID, holdReason); err != nil {
			return err
		}
		return s.repository.CreateQuarantineDecisionTx(tx, quarantine, actor, requestID, holdReason)
	})
	if txErr != nil {
		if errors.Is(txErr, repository.ErrVersionConflict) {
			return model.CalibrationRequest{}, repository.ErrVersionConflict
		}
		return model.CalibrationRequest{}, fmt.Errorf("resolve 色彩复校准申请: %w", txErr)
	}

	if err := s.security.Audit(ctx, actor, requestID, "calibrate", "CalibrationRequest", id, model.CalibrationRequestInitialStatus, result,
		fmt.Sprintf("retest measured ΔE %.2f vs target %.2f", input.MeasuredDelta, current.TargetDelta)); err != nil {
		return model.CalibrationRequest{}, fmt.Errorf("persist calibration audit: %w", err)
	}
	if !passed {
		if err := s.security.Audit(ctx, actor, requestID, "transition", "PrintRun", heldRun.ID, string(constants.RunStateProofing), string(constants.RunStateHold),
			fmt.Sprintf("held after failed calibration %s", current.Code)); err != nil {
			return model.CalibrationRequest{}, fmt.Errorf("persist run hold audit: %w", err)
		}
		if err := s.security.Audit(ctx, actor, requestID, "create", "ReleaseDecision", quarantine.ID, "", string(constants.DecisionTypeQuarantine),
			fmt.Sprintf("quarantine generated by failed calibration %s", current.Code)); err != nil {
			return model.CalibrationRequest{}, fmt.Errorf("persist quarantine audit: %w", err)
		}
	}
	return s.repository.Get(ctx, id)
}

func (s *calibrationService) quarantineDecision(run model.PrintRun, calibration model.CalibrationRequest, actor string) *model.ReleaseDecision {
	code := "QD-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	return &model.ReleaseDecision{
		BaseModel: model.BaseModel{
			Code:        code,
			Name:        "隔离决定 · " + run.Code,
			Status:      string(constants.DecisionTypeQuarantine),
			Version:     1,
			Description: fmt.Sprintf("批次 %s 复校准 %s 超差自动隔离", run.Code, calibration.Code),
		},
		Facility: run.Facility, Owner: actor, Category: run.Category, RiskLevel: run.RiskLevel,
		MetricValue: 0, MetricUnit: "ΔE", EffectiveAt: time.Now().UTC(),
		Evidence: calibration.Evidence, RelatedCode: run.Code,
	}
}
