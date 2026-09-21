package dto

import "time"

// CreateCalibrationRequest is the scheduling contract a reviewer submits after
// a print run enters proofing. Status/result are server-controlled.
type CreateCalibrationRequest struct {
	Code        string    `json:"code" binding:"omitempty,max=64"`
	PrintRunID  uint      `json:"printRunId" binding:"required"`
	PressID     uint      `json:"pressId" binding:"required"`
	TargetDelta float64   `json:"targetDelta"`
	Sample      string    `json:"sample" binding:"required,min=2,max=240"`
	RetestDueAt time.Time `json:"retestDueAt" binding:"required"`
	Evidence    string    `json:"evidence" binding:"max=2000"`
	Reason      string    `json:"reason" binding:"required,min=3,max=500"`
}

// CompleteCalibrationRequest is the retest result contract. The service
// compares the measured delta with the registered target and derives the
// result; a stale or duplicate completion fails the optimistic lock.
type CompleteCalibrationRequest struct {
	ExpectedVersion uint    `json:"expectedVersion" binding:"required"`
	MeasuredDelta   float64 `json:"measuredDelta"`
	Evidence        string  `json:"evidence" binding:"max=2000"`
	Reason          string  `json:"reason" binding:"required,min=3,max=500"`
}

// CalibrationQuery supports the run/proof/release pages with run and status
// filters while reusing the standard paging envelope.
type CalibrationQuery struct {
	Page       int    `form:"page"`
	PageSize   int    `form:"pageSize"`
	Search     string `form:"search"`
	Status     string `form:"status"`
	PrintRunID uint   `form:"printRunId"`
	RunCode    string `form:"runCode"`
}
