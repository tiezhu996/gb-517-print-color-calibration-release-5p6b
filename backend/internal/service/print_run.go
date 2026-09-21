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
	"gorm.io/gorm"
)

type PrintRunService interface {
	List(context.Context, dto.PageQuery) (repository.Page[model.PrintRun], error)
	Get(context.Context, uint) (model.PrintRun, error)
	Create(context.Context, dto.CreatePrintRun, string, string) (model.PrintRun, error)
	Update(context.Context, uint, dto.UpdatePrintRun, string, string) (model.PrintRun, error)
	Transition(context.Context, uint, dto.TransitionRequest, string, string, string) (model.PrintRun, error)
	Delete(context.Context, uint, string, string) error
	StatusCounts(context.Context) (map[string]int64, error)
}

// calibrationGate exposes the latest re-calibration request of a run so the
// run state machine can enforce the release closed loop.
type calibrationGate interface {
	LatestForRun(context.Context, uint) (model.CalibrationRequest, error)
}

type printRunService struct {
	repository repository.PrintRunRepository
	security   SecurityService
	gate       calibrationGate
}

func NewPrintRunService(repo repository.PrintRunRepository, security SecurityService, gate calibrationGate) PrintRunService {
	return &printRunService{repository: repo, security: security, gate: gate}
}

func (s *printRunService) List(ctx context.Context, query dto.PageQuery) (repository.Page[model.PrintRun], error) {
	return s.repository.List(ctx, query)
}

func (s *printRunService) Get(ctx context.Context, id uint) (model.PrintRun, error) {
	return s.repository.Get(ctx, id)
}

func (s *printRunService) Create(ctx context.Context, input dto.CreatePrintRun, actor, requestID string) (model.PrintRun, error) {
	if err := validatePrintRunBusinessFields(input.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.PrintRun{}, err
	}
	item := model.PrintRun{
		BaseModel: model.BaseModel{
			Code: strings.ToUpper(strings.TrimSpace(input.Code)), Name: strings.TrimSpace(input.Name),
			Status: model.PrintRunInitialStatus, Version: 1, Description: strings.TrimSpace(input.Description),
		},
		Facility: strings.TrimSpace(input.Facility), Owner: strings.TrimSpace(input.Owner),
		Category: strings.TrimSpace(input.Category), RiskLevel: input.RiskLevel,
		MetricValue: input.MetricValue, MetricUnit: strings.TrimSpace(input.MetricUnit),
		EffectiveAt: input.EffectiveAt.UTC(), Evidence: strings.TrimSpace(input.Evidence),
		RelatedCode: strings.ToUpper(strings.TrimSpace(input.RelatedCode)),
	}
	if err := s.repository.CreateVersioned(ctx, &item, actor, requestID, "created colour configuration"); err != nil {
		return model.PrintRun{}, fmt.Errorf("create 印刷批次: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "create", "PrintRun", item.ID, "", item.Status, "created 印刷批次")
	return item, nil
}

func (s *printRunService) Update(ctx context.Context, id uint, input dto.UpdatePrintRun, actor, requestID string) (model.PrintRun, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.PrintRun{}, err
	}
	if err := validatePrintRunBusinessFields(current.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.PrintRun{}, err
	}
	current.Name = strings.TrimSpace(input.Name)
	current.Description = strings.TrimSpace(input.Description)
	current.Facility = strings.TrimSpace(input.Facility)
	current.Owner = strings.TrimSpace(input.Owner)
	current.Category = strings.TrimSpace(input.Category)
	current.RiskLevel = input.RiskLevel
	current.MetricValue = input.MetricValue
	current.MetricUnit = strings.TrimSpace(input.MetricUnit)
	current.EffectiveAt = input.EffectiveAt.UTC()
	current.Evidence = strings.TrimSpace(input.Evidence)
	current.RelatedCode = strings.ToUpper(strings.TrimSpace(input.RelatedCode))
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateVersioned(ctx, id, input.ExpectedVersion, &current, actor, requestID, "updated colour configuration"); err != nil {
		return model.PrintRun{}, fmt.Errorf("update 印刷批次: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "update", "PrintRun", id, current.Status, current.Status, "updated business fields")
	return s.repository.Get(ctx, id)
}

func (s *printRunService) Transition(ctx context.Context, id uint, input dto.TransitionRequest, actor, role, requestID string) (model.PrintRun, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.PrintRun{}, err
	}
	target := strings.TrimSpace(input.Status)
	if (target == string(constants.RunStateReleased) || current.Status == string(constants.RunStateReleased)) && !canReview(role) {
		return model.PrintRun{}, ErrForbidden
	}
	if !constants.CanTransition(constants.PrintRunTransitions, current.Status, target) {
		return model.PrintRun{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.Status, target)
	}
	if s.gate != nil {
		if latest, err := s.gate.LatestForRun(ctx, id); err == nil {
			// A pending re-calibration freezes the batch until the retest is
			// recorded; a failed one must return to proofing and pass before
			// release. Rejected transitions never mutate run state.
			switch {
			case latest.Status == model.CalibrationRequestInitialStatus:
				return model.PrintRun{}, ErrCalibrationBlocked
			case latest.Status == string(constants.CalibrationStatusFailed) && target == string(constants.RunStateReleased):
				return model.PrintRun{}, ErrCalibrationBlocked
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return model.PrintRun{}, err
		}
	}
	before := current.Status
	current.Status = target
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateVersioned(ctx, id, input.ExpectedVersion, &current, actor, requestID, input.Reason); err != nil {
		return model.PrintRun{}, fmt.Errorf("transition 印刷批次: %w", err)
	}
	if err := s.security.Audit(ctx, actor, requestID, "transition", "PrintRun", id, before, target, input.Reason); err != nil {
		return model.PrintRun{}, fmt.Errorf("persist transition audit: %w", err)
	}
	return s.repository.Get(ctx, id)
}

func canReview(role string) bool { return role == model.RoleReviewer || role == model.RoleAdmin }

func (s *printRunService) Delete(ctx context.Context, id uint, actor, requestID string) error {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.Status != model.PrintRunInitialStatus {
		return ErrLocked
	}
	if err := s.repository.Delete(ctx, id); err != nil {
		return err
	}
	return s.security.Audit(ctx, actor, requestID, "delete", "PrintRun", id, current.Status, "deleted", "soft deleted 印刷批次")
}

func (s *printRunService) StatusCounts(ctx context.Context) (map[string]int64, error) {
	return s.repository.CountByStatus(ctx)
}

func validatePrintRunBusinessFields(code, name, facility, owner string) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(facility) == "" || strings.TrimSpace(owner) == "" {
		return ErrInvalidInput
	}
	return nil
}
