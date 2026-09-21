package model

import "time"

// CalibrationRequest models 批次色彩复校准申请. A proofing batch can have at
// most one pending request, so retest scheduling is never duplicated. The
// aggregate keeps its own optimistic lock and an append-only evidence chain so
// concurrent completion can succeed at most once and measured evidence can
// never be overwritten.
type CalibrationRequest struct {
	BaseModel
	PrintRunID     uint       `json:"printRunId" gorm:"not null;index"`
	PrintRunCode   string     `json:"printRunCode" gorm:"size:64;index"`
	PressID        uint       `json:"pressId" gorm:"not null;index"`
	PressCode      string     `json:"pressCode" gorm:"size:64;index"`
	TargetDelta    float64    `json:"targetDelta"`
	Sample         string     `json:"sample" gorm:"size:240;not null"`
	RetestDueAt    time.Time  `json:"retestDueAt" gorm:"not null;index"`
	MeasuredDelta  *float64   `json:"measuredDelta"`
	Result         string     `json:"result" gorm:"size:40"`
	Evidence       string     `json:"evidence" gorm:"size:2000"`
	ResolvedBy     string     `json:"resolvedBy" gorm:"size:80;index"`
	ResolvedAt     *time.Time `json:"resolvedAt"`
	QuarantineCode string     `json:"quarantineCode" gorm:"size:64;index"`
	// PendingSlot is non-null only while the request is pending. The unique
	// index enforces "at most one pending request per run" across concurrent
	// requests on MySQL/SQLite without partial indexes.
	PendingSlot *string               `json:"-" gorm:"size:64;uniqueIndex"`
	Revisions   []CalibrationRevision `json:"revisions,omitempty" gorm:"foreignKey:CalibrationRequestID"`
}

func (item *CalibrationRequest) GetBase() *BaseModel { return &item.BaseModel }

func (item CalibrationRequest) TableName() string { return "calibration_requests" }

var CalibrationRequestInitialStatus = "pending"

// CalibrationRevision preserves the immutable evidence of each calibration
// stage (scheduling, retest, reopen) together with actor and request id.
type CalibrationRevision struct {
	ID                   uint      `json:"id" gorm:"primaryKey"`
	CalibrationRequestID uint      `json:"calibrationRequestId" gorm:"not null;uniqueIndex:idx_calibration_revision"`
	Version              uint      `json:"version" gorm:"not null;uniqueIndex:idx_calibration_revision"`
	Status               string    `json:"status" gorm:"size:40;not null"`
	TargetDelta          float64   `json:"targetDelta"`
	Sample               string    `json:"sample" gorm:"size:240;not null"`
	RetestDueAt          time.Time `json:"retestDueAt" gorm:"not null"`
	MeasuredDelta        *float64  `json:"measuredDelta"`
	Result               string    `json:"result" gorm:"size:40"`
	Evidence             string    `json:"evidence" gorm:"size:2000"`
	PressCode            string    `json:"pressCode" gorm:"size:64"`
	PrintRunCode         string    `json:"printRunCode" gorm:"size:64"`
	QuarantineCode       string    `json:"quarantineCode" gorm:"size:64"`
	Actor                string    `json:"actor" gorm:"size:80;not null"`
	RequestID            string    `json:"requestId" gorm:"size:80;not null"`
	Reason               string    `json:"reason" gorm:"size:500;not null"`
	CreatedAt            time.Time `json:"createdAt"`
}
