package model

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// ReviewStatus is the lifecycle state of a ReviewRequest.
type ReviewStatus string

const (
	// ReviewPending is the initial state: a human must still decide.
	ReviewPending ReviewStatus = "pending"
	// ReviewApproved means a human (or any frontend) approved the subject.
	ReviewApproved ReviewStatus = "approved"
	// ReviewRejected means the subject was rejected.
	ReviewRejected ReviewStatus = "rejected"
	// ReviewExpired means the optional ExpiresAt passed before a decision.
	ReviewExpired ReviewStatus = "expired"
	// ReviewCanceled means the request was withdrawn before resolution.
	ReviewCanceled ReviewStatus = "canceled"
)

// ReviewRequest is a generic, UI-agnostic "a human must decide X" record. It is
// deliberately NOT planning-specific: the subject is described by Kind + RefID,
// so the same primitive serves plan-step approvals, tool-call gates, payments,
// or any custom approval, presented and resolved by any frontend (Telegram, the
// debug dashboard, an HTTP API). See docs/improvements/10-human-in-the-loop.md.
type ReviewRequest struct {
	ID        string `json:"id"`         // stable id (crypto/rand), e.g. "rev_<hex>"
	UserID    string `json:"user_id"`    // who owns / must make the decision
	SessionID string `json:"session_id"` // optional originating session

	// Subject — what is being approved, kept generic (never plan-specific):
	Kind    string   `json:"kind"`              // e.g. "plan_step", "tool_call", "payment", "custom"
	RefID   string   `json:"ref_id"`            // id of the subject (e.g. "<planID>/<stepID>")
	Title   string   `json:"title"`             // short human prompt
	Content string   `json:"content"`           // detail shown to the approver
	Options []string `json:"options,omitempty"` // choices; default ["approve","reject"]

	Status    ReviewStatus   `json:"status"`
	Decision  string         `json:"decision,omitempty"`   // chosen option once resolved
	Note      string         `json:"note,omitempty"`       // approver's note
	DecidedBy string         `json:"decided_by,omitempty"` // who resolved it
	CreatedAt time.Time      `json:"created_at"`
	DecidedAt time.Time      `json:"decided_at,omitempty"`
	ExpiresAt time.Time      `json:"expires_at,omitempty"` // optional auto-expire
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// NewReviewID returns a fresh crypto/rand review id ("rev_" + 16 hex chars).
func NewReviewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "rev_" + hex.EncodeToString(b)
}

// NewReviewRequest builds a pending review for the given subject with sensible
// defaults (random id, pending status, approve/reject options, CreatedAt now).
// Callers may set UserID/SessionID/Title/Content/ExpiresAt/Metadata afterwards.
func NewReviewRequest(kind, refID string) *ReviewRequest {
	return &ReviewRequest{
		ID:        NewReviewID(),
		Kind:      kind,
		RefID:     refID,
		Status:    ReviewPending,
		Options:   []string{"approve", "reject"},
		CreatedAt: time.Now(),
	}
}

// IsResolved reports whether the review has reached a terminal state (anything
// other than pending).
func (r *ReviewRequest) IsResolved() bool {
	return r != nil && r.Status != ReviewPending
}

// IsApproved reports whether the review was resolved as approved.
func (r *ReviewRequest) IsApproved() bool {
	return r != nil && r.Status == ReviewApproved
}
