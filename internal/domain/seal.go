package domain

import (
	"fmt"
	"strings"
	"time"
)

type EvidenceSeal struct {
	ID            string             `json:"id"`
	CaseID        string             `json:"case_id"`
	DeviceID      string             `json:"device_id"`
	MeasurementID string             `json:"measurement_id"`
	Reviewer      string             `json:"reviewer"`
	Summary       string             `json:"summary"`
	SealedAt      time.Time          `json:"sealed_at"`
	Authority     QualificationBasis `json:"authority"`
}

func NewEvidenceSeal(id, caseID, deviceID, measurementID, reviewer, summary string, at time.Time, authority QualificationBasis) (EvidenceSeal, error) {
	item := EvidenceSeal{
		ID:            strings.TrimSpace(id),
		CaseID:        strings.TrimSpace(caseID),
		DeviceID:      strings.TrimSpace(deviceID),
		MeasurementID: strings.TrimSpace(measurementID),
		Reviewer:      strings.TrimSpace(reviewer),
		Summary:       strings.TrimSpace(summary),
		SealedAt:      at.UTC(),
		Authority:     authority,
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
	if err := s.Authority.Validate(); err != nil {
		return fmt.Errorf("%w: seal authority basis: %v", ErrInvalidInput, err)
	}
	if s.Authority.Reviewer != s.Reviewer {
		return fmt.Errorf("%w: seal reviewer does not match qualification basis", ErrInvalidInput)
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

// Authorizes reports whether this qualification lets the reviewer sign at the
// given moment. The reviewer must match, the qualification must be active, and
// the instant must lie inside the validity window, including both endpoints.
func (q ReviewerQualification) Authorizes(reviewer string, at time.Time) bool {
	if strings.TrimSpace(reviewer) != q.Reviewer {
		return false
	}
	if q.Status != QualificationActive {
		return false
	}
	stamp := at.UTC()
	if stamp.Before(q.ValidFrom) || stamp.After(q.ValidUntil) {
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

// QualificationBasis is the immutable authority basis frozen into an evidence
// seal at the instant sealing is decided. It is intentionally a value snapshot:
// later suspension or expiry of the live qualification must never rewrite the
// basis on which a completed seal was issued.
type QualificationBasis struct {
	ID         string    `json:"id"`
	Reviewer   string    `json:"reviewer"`
	Scope      string    `json:"scope"`
	ValidFrom  time.Time `json:"valid_from"`
	ValidUntil time.Time `json:"valid_until"`
	CapturedAt time.Time `json:"captured_at"`
}

// NewQualificationBasis snapshots a qualification only if it authorizes the
// reviewer at the given decision instant.
func NewQualificationBasis(q ReviewerQualification, reviewer string, at time.Time) (QualificationBasis, error) {
	if !q.Authorizes(reviewer, at) {
		return QualificationBasis{}, fmt.Errorf("%w: reviewer %s does not hold a valid qualification", ErrStateConflict, reviewer)
	}
	basis := QualificationBasis{
		ID:         q.ID,
		Reviewer:   q.Reviewer,
		Scope:      q.Scope,
		ValidFrom:  q.ValidFrom,
		ValidUntil: q.ValidUntil,
		CapturedAt: at.UTC(),
	}
	if err := basis.Validate(); err != nil {
		return QualificationBasis{}, err
	}
	return basis, nil
}

func (b QualificationBasis) Validate() error {
	if b.ID == "" || b.Reviewer == "" || b.Scope == "" {
		return fmt.Errorf("%w: qualification id, reviewer, and scope are required", ErrInvalidInput)
	}
	if b.ValidFrom.IsZero() || b.ValidUntil.IsZero() || !b.ValidUntil.After(b.ValidFrom) {
		return fmt.Errorf("%w: qualification validity window is invalid", ErrInvalidInput)
	}
	if b.CapturedAt.IsZero() || b.CapturedAt.Before(b.ValidFrom) || b.CapturedAt.After(b.ValidUntil) {
		return fmt.Errorf("%w: basis capture time must fall inside the validity window", ErrInvalidInput)
	}
	return nil
}
