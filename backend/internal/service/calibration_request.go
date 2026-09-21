package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blueship581/print-color-calibration-release/backend/internal/constants"
	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/model"
	"github.com/blueship581/print-color-calibration-release/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxCalibrationDeadline = 30 * 24 * time.Hour

// CalibrationService implements the batch colour re-calibration closed loop:
// reviewer-only registration, one pending request per batch, validated
// equipment/deadline, and a one-shot retest backfill.
type CalibrationService interface {
	List(context.Context, dto.CalibrationPageQuery) (repository.Page[model.CalibrationRequest], error)
	Get(context.Context, uint) (model.CalibrationRequest, error)
	Create(context.Context, dto.CreateCalibrationRequest, string, string, string) (model.CalibrationRequest, error)
	Complete(context.Context, uint, dto.CompleteCalibrationRequest, string, string, string) (model.CalibrationRequest, error)
}

type calibrationService struct {
	calibrations repository.CalibrationRepository
	runs         repository.PrintRunRepository
	presses      repository.PressUnitRepository
	security     SecurityService
}

func NewCalibrationService(calibrations repository.CalibrationRepository, runs repository.PrintRunRepository, presses repository.PressUnitRepository, security SecurityService) CalibrationService {
	return &calibrationService{calibrations: calibrations, runs: runs, presses: presses, security: security}
}

func (s *calibrationService) List(ctx context.Context, query dto.CalibrationPageQuery) (repository.Page[model.CalibrationRequest], error) {
	return s.calibrations.List(ctx, query)
}

func (s *calibrationService) Get(ctx context.Context, id uint) (model.CalibrationRequest, error) {
	item, err := s.calibrations.Get(ctx, id)
	if err != nil {
		return item, err
	}
	s.decorate(ctx, &item)
	return item, nil
}

// Create registers a request; rejections precede any write.
func (s *calibrationService) Create(ctx context.Context, input dto.CreateCalibrationRequest, actor, role, requestID string) (model.CalibrationRequest, error) {
	if !canReview(role) {
		return model.CalibrationRequest{}, ErrForbidden
	}
	run, err := s.runs.Get(ctx, input.PrintRunID)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	if run.Status != string(constants.RunStateProofing) {
		return model.CalibrationRequest{}, fmt.Errorf("%w: batch must be proofing to open calibration", ErrInvalidTransition)
	}
	if _, err := s.calibrations.FindPendingForRun(ctx, input.PrintRunID); err == nil {
		return model.CalibrationRequest{}, ErrPendingCalibration
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CalibrationRequest{}, err
	}
	press, err := s.presses.Get(ctx, input.PressUnitID)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	if !pressUsableStatus[press.Status] {
		return model.CalibrationRequest{}, fmt.Errorf("%w: press %s is %s", ErrPressUnavailable, press.Code, press.Status)
	}
	deadline := input.RetestDueAt.UTC()
	if !validDeadline(deadline) {
		return model.CalibrationRequest{}, ErrInvalidDeadline
	}
	samples := strings.TrimSpace(input.Samples)
	if samples == "" {
		return model.CalibrationRequest{}, ErrInvalidInput
	}

	pendingRunID := run.ID
	request := model.CalibrationRequest{
		Code: generateCalibrationCode(), PrintRunID: run.ID, PressUnitID: press.ID,
		PressCode: press.Code, PressName: press.Name, Samples: samples,
		TargetDelta: input.TargetDelta, RetestDueAt: deadline,
		Status: model.CalibrationInitialState, Version: 1, PendingRunID: &pendingRunID,
		CreatedBy: actor, RequestID: requestID,
	}
	// Unique pending_run_id ensures parallel inserts win once.
	for attempt := 0; attempt < 3; attempt++ {
		if err = s.calibrations.Insert(ctx, &request); err == nil {
			break
		}
		if errors.Is(err, repository.ErrDuplicatePending) {
			return model.CalibrationRequest{}, ErrPendingCalibration
		}
		if !errors.Is(err, repository.ErrCodeConflict) {
			return model.CalibrationRequest{}, fmt.Errorf("create 色彩复校准申请: %w", err)
		}
		request.Code = generateCalibrationCode()
	}
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	_ = s.security.Audit(ctx, actor, requestID, "calibration-create", "CalibrationRequest", request.ID, "", request.Status,
		fmt.Sprintf("opened recalibration for run %s on press %s; target ΔE %.2f; due %s", run.Code, press.Code, request.TargetDelta, deadline.Format(time.RFC3339)))
	return request, nil
}

// Complete backfills the retest. The verdict must agree with measured vs
// target; conditional updates make repeated/concurrent calls win once and
// keep evidence immutable.
func (s *calibrationService) Complete(ctx context.Context, id uint, input dto.CompleteCalibrationRequest, actor, role, requestID string) (model.CalibrationRequest, error) {
	if !canReview(role) {
		return model.CalibrationRequest{}, ErrForbidden
	}
	request, err := s.calibrations.Get(ctx, id)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	if request.Status != string(constants.CalibrationStatePending) {
		return model.CalibrationRequest{}, ErrCalibrationSettled
	}
	if input.ExpectedVersion != request.Version {
		return model.CalibrationRequest{}, repository.ErrVersionConflict
	}
	derived := constants.CalibrationStatePassed
	if input.MeasuredDelta > request.TargetDelta {
		derived = constants.CalibrationStateFailed
	}
	if input.Result != string(derived) {
		return model.CalibrationRequest{}, fmt.Errorf("%w: measured ΔE %.2f vs target ΔE %.2f", ErrResultMismatch, input.MeasuredDelta, request.TargetDelta)
	}

	now := time.Now().UTC()
	note := strings.TrimSpace(input.ResultNote)
	if derived == constants.CalibrationStatePassed {
		if err := s.calibrations.CompletePassed(ctx, id, input.ExpectedVersion, input.MeasuredDelta, note, actor, now); err != nil {
			return model.CalibrationRequest{}, mapCompleteError(err)
		}
		_ = s.security.Audit(ctx, actor, requestID, "calibration-complete", "CalibrationRequest", id,
			string(constants.CalibrationStatePending), string(constants.CalibrationStatePassed),
			fmt.Sprintf("retest passed for run #%d; measured ΔE %.2f within target ΔE %.2f", request.PrintRunID, input.MeasuredDelta, request.TargetDelta))
		completed, getErr := s.calibrations.Get(ctx, id)
		if getErr != nil {
			return model.CalibrationRequest{}, getErr
		}
		s.decorate(ctx, &completed)
		return completed, nil
	}

	// Out of tolerance: settle, hold the batch and quarantine in one transaction.
	run, err := s.runs.Get(ctx, request.PrintRunID)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	if run.Status != string(constants.RunStateProofing) {
		return model.CalibrationRequest{}, fmt.Errorf("%w: batch left proofing before retest settled", ErrInvalidTransition)
	}
	measured := input.MeasuredDelta
	request.MeasuredDelta = &measured
	decision := quarantineDecisionFor(&run, &request, measured, actor, now)
	if err := s.calibrations.CompleteFailed(ctx, &request, &run, decision, note, actor, requestID, now); err != nil {
		return model.CalibrationRequest{}, mapCompleteError(err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "calibration-complete", "CalibrationRequest", id,
		string(constants.CalibrationStatePending), string(constants.CalibrationStateFailed),
		fmt.Sprintf("retest failed; measured ΔE %.2f exceeded target ΔE %.2f; run %s held", measured, request.TargetDelta, run.Code))
	_ = s.security.Audit(ctx, actor, requestID, "transition", "PrintRun", run.ID,
		string(constants.RunStateProofing), string(constants.RunStateHold), "auto hold after calibration out of tolerance")
	_ = s.security.Audit(ctx, actor, requestID, "create", "ReleaseDecision", decision.ID, "",
		string(constants.DecisionTypeQuarantine), fmt.Sprintf("auto quarantine %s from failed calibration %s", decision.Code, request.Code))

	completed, err := s.calibrations.Get(ctx, id)
	if err != nil {
		return model.CalibrationRequest{}, err
	}
	completed.QuarantineCode = decision.Code
	decisionID := decision.ID
	completed.QuarantineDecisionID = &decisionID
	s.decorate(ctx, &completed)
	return completed, nil
}

// decorate fills the transient run status for detail views.
func (s *calibrationService) decorate(ctx context.Context, item *model.CalibrationRequest) {
	if run, err := s.runs.Get(ctx, item.PrintRunID); err == nil {
		item.RunStatus = run.Status
	}
}

var pressUsableStatus = map[string]bool{"ready": true, "setup": true, "printing": true}

func validDeadline(deadline time.Time) bool {
	now := time.Now().UTC()
	return deadline.After(now) && !deadline.After(now.Add(maxCalibrationDeadline))
}

func generateCalibrationCode() string { return calibrationCode("CAL-") }
func generateDecisionCode() string    { return calibrationCode("RD-CAL-") }
func calibrationCode(prefix string) string {
	return prefix + strings.ReplaceAll(strings.ToUpper(uuid.NewString()[:8]), "-", "")
}

// quarantineDecisionFor assembles the immutable over-tolerance decision.
func quarantineDecisionFor(run *model.PrintRun, calibration *model.CalibrationRequest, measured float64, actor string, now time.Time) *model.ReleaseDecision {
	metricUnit := strings.TrimSpace(run.MetricUnit)
	if metricUnit == "" {
		metricUnit = "ΔE"
	}
	if owner := strings.TrimSpace(actor); owner == "" {
		actor = "reviewer"
	}
	return &model.ReleaseDecision{
		BaseModel: model.BaseModel{
			Code: generateDecisionCode(), Name: "隔离决定 · " + run.Name,
			Status: string(constants.DecisionTypeQuarantine), Version: 1,
			Description: fmt.Sprintf("复校准 %s 复测超差自动生成的隔离决定", calibration.Code),
		},
		Facility: run.Facility, Owner: actor, Category: run.Category, RiskLevel: "high",
		MetricValue: measured, MetricUnit: metricUnit, EffectiveAt: now,
		Evidence: fmt.Sprintf("复校准 %s：实测 ΔE %.2f，目标 ΔE %.2f，样本 %s，复测设备 %s（%s），复测期限 %s",
			calibration.Code, measured, calibration.TargetDelta, calibration.Samples,
			calibration.PressCode, calibration.PressName, calibration.RetestDueAt.Format(time.RFC3339)),
		RelatedCode: run.Code,
	}
}

func mapCompleteError(err error) error {
	switch {
	case errors.Is(err, repository.ErrAlreadySettled):
		return ErrCalibrationSettled
	case errors.Is(err, repository.ErrVersionConflict):
		return repository.ErrVersionConflict
	default:
		return fmt.Errorf("complete 色彩复校准: %w", err)
	}
}
