package domain

import (
	"fmt"
	"strings"
	"time"
)

type EvidenceSeal struct {
	ID              string    `json:"id"`
	CaseID          string    `json:"case_id"`
	DeviceID        string    `json:"device_id"`
	MeasurementID   string    `json:"measurement_id"`
	Reviewer        string    `json:"reviewer"`
	Summary         string    `json:"summary"`
	SealedAt        time.Time `json:"sealed_at"`
	QualificationID string    `json:"qualification_id,omitempty"`
}

func NewEvidenceSeal(id, caseID, deviceID, measurementID, reviewer, summary string, at time.Time) (EvidenceSeal, error) {
	item := EvidenceSeal{
		ID:            strings.TrimSpace(id),
		CaseID:        strings.TrimSpace(caseID),
		DeviceID:      strings.TrimSpace(deviceID),
		MeasurementID: strings.TrimSpace(measurementID),
		Reviewer:      strings.TrimSpace(reviewer),
		Summary:       strings.TrimSpace(summary),
		SealedAt:      at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return EvidenceSeal{}, err
	}
	return item, nil
}

func (s EvidenceSeal) Validate() error {
	if s.ID == "" || s.CaseID == "" || s.DeviceID == "" || s.MeasurementID == "" || s.Reviewer == "" || s.Summary == "" {
		return fmt.Errorf("%w: seal id, case, device, measurement, reviewer, and summary are required", ErrInvalidInput)
	}
	if s.SealedAt.IsZero() {
		return fmt.Errorf("%w: seal time is required", ErrInvalidInput)
	}
	return nil
}

type QualificationStatus string

const (
	QualificationActive    QualificationStatus = "active"
	QualificationSuspended QualificationStatus = "suspended"
)

// ReviewerQualification bounds which reviewer may sign off and for how long.
type ReviewerQualification struct {
	ID         string              `json:"id"`
	Reviewer   string              `json:"reviewer"`
	Scope      string              `json:"scope"`
	Status     QualificationStatus `json:"status"`
	ValidFrom  time.Time           `json:"valid_from"`
	ValidUntil time.Time           `json:"valid_until"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

func NewReviewerQualification(id, reviewer, scope string, validFrom, validUntil, at time.Time) (ReviewerQualification, error) {
	item := ReviewerQualification{
		ID:         strings.TrimSpace(id),
		Reviewer:   strings.TrimSpace(reviewer),
		Scope:      strings.TrimSpace(scope),
		Status:     QualificationActive,
		ValidFrom:  validFrom.UTC(),
		ValidUntil: validUntil.UTC(),
		CreatedAt:  at.UTC(),
		UpdatedAt:  at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return ReviewerQualification{}, err
	}
	return item, nil
}

func (q ReviewerQualification) Validate() error {
	if q.ID == "" || q.Reviewer == "" || q.Scope == "" {
		return fmt.Errorf("%w: qualification id, reviewer, and scope are required", ErrInvalidInput)
	}
	if q.ValidFrom.IsZero() || q.ValidUntil.IsZero() || !q.ValidUntil.After(q.ValidFrom) {
		return fmt.Errorf("%w: qualification validity window is invalid", ErrInvalidInput)
	}
	if q.CreatedAt.IsZero() || q.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: qualification timestamps are required", ErrInvalidInput)
	}
	switch q.Status {
	case QualificationActive, QualificationSuspended:
		return nil
	default:
		return fmt.Errorf("%w: unsupported qualification state %q", ErrInvalidInput, q.Status)
	}
}

// Authorizes reports whether this qualification lets the reviewer sign at the given moment.
func (q ReviewerQualification) Authorizes(reviewer string, at time.Time) bool {
	if strings.TrimSpace(reviewer) != q.Reviewer {
		return false
	}
	if q.Status != QualificationActive {
		return false
	}
	if at.Before(q.ValidFrom) {
		return false
	}
	return true
}

func (q *ReviewerQualification) Suspend(at time.Time) error {
	if q.Status != QualificationActive {
		return fmt.Errorf("%w: qualification is not active", ErrStateConflict)
	}
	q.Status = QualificationSuspended
	q.UpdatedAt = at.UTC()
	return nil
}
