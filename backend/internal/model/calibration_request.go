package model

import "time"

// CalibrationRequest models 批次色彩复校准申请. A proofing batch can have at
// most one pending request. PendingRunID is set while the request is pending
// and cleared once the retest is backfilled, so a partial unique index is
// avoided and the constraint works on both MySQL and SQLite. Re-calibrating a
// batch after a finished request creates a fresh row rather than mutating
// evidence, which keeps every measurement immutable.
type CalibrationRequest struct {
	ID                   uint       `json:"id" gorm:"primaryKey"`
	Code                 string     `json:"code" gorm:"size:64;uniqueIndex;not null"`
	PrintRunID           uint       `json:"printRunId" gorm:"not null;index:idx_calibration_run"`
	PressUnitID          uint       `json:"pressUnitId" gorm:"not null;index"`
	PressCode            string     `json:"pressCode" gorm:"size:64;not null"`
	PressName            string     `json:"pressName" gorm:"size:160;not null"`
	Samples              string     `json:"samples" gorm:"size:2000;not null"`
	TargetDelta          float64    `json:"targetDelta" gorm:"not null"`
	RetestDueAt          time.Time  `json:"retestDueAt" gorm:"not null;index"`
	Status               string     `json:"status" gorm:"size:40;index;not null"`
	Version              uint       `json:"version" gorm:"not null;default:1"`
	MeasuredDelta        *float64   `json:"measuredDelta" gorm:"column:measured_delta"`
	ResultNote           string     `json:"resultNote" gorm:"size:2000"`
	CompletedAt          *time.Time `json:"completedAt"`
	CompletedBy          string     `json:"completedBy" gorm:"size:80"`
	QuarantineDecisionID *uint      `json:"quarantineDecisionId,omitempty" gorm:"index"`
	PendingRunID         *uint      `json:"-" gorm:"uniqueIndex"`
	CreatedBy            string     `json:"createdBy" gorm:"size:80;not null"`
	RequestID            string     `json:"requestId" gorm:"size:80;not null"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`

	// Transient display fields populated by the service layer; never persisted.
	Deviation      *float64 `json:"deviation" gorm:"-"`
	RunStatus      string   `json:"runStatus,omitempty" gorm:"-"`
	QuarantineCode string   `json:"quarantineCode,omitempty" gorm:"-"`
}

func (CalibrationRequest) TableName() string { return "calibration_requests" }

var CalibrationInitialState = "pending"

// CalibrationStateTransitions documents the one-shot completion rule used by
// audits and service validation. Pending is the only state that accepts
// backfill; passed/failed are terminal so retries can never overwrite
// evidence.
var CalibrationStateTransitions = map[string]map[string]bool{
	"pending": {"passed": true, "failed": true},
	"passed":  {},
	"failed":  {},
}
