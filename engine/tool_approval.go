package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/model"
)

// ToolApprovalManager is the narrow approval contract needed by the execution
// engine. review.Manager satisfies it without coupling the engine to a
// particular UI, transport, or approval implementation.
type ToolApprovalManager interface {
	Request(ctx context.Context, r *model.ReviewRequest) (string, error)
	Await(ctx context.Context, id string) (*model.ReviewRequest, error)
}

// ToolApprovalRequest describes one concrete tool invocation that is about to
// run. Arguments must be the original model-supplied JSON, without internal
// framework fields such as __user_id__.
type ToolApprovalRequest struct {
	RefID       string
	UserID      string
	SessionID   string
	AgentType   model.AgentType
	ToolName    string
	DisplayName string
	Arguments   string
}

// ErrToolApprovalRejected marks a human decision that denied a tool call.
// errors.Is can be used by hosts that want to distinguish a rejection from an
// approval-system failure.
var ErrToolApprovalRejected = errors.New("tool approval rejected")

// ToolApprovalRejectedError records the review that denied execution.
type ToolApprovalRejectedError struct {
	ReviewID string
	Decision string
	Note     string
}

func (e *ToolApprovalRejectedError) Error() string {
	if e == nil {
		return ErrToolApprovalRejected.Error()
	}
	msg := fmt.Sprintf("%s (review_id=%s", ErrToolApprovalRejected, e.ReviewID)
	if e.Decision != "" {
		msg += ", decision=" + e.Decision
	}
	msg += ")"
	if strings.TrimSpace(e.Note) != "" {
		msg += ": " + e.Note
	}
	return msg
}

func (e *ToolApprovalRejectedError) Unwrap() error { return ErrToolApprovalRejected }

// AwaitToolApproval raises and waits for a human decision. A nil manager means
// approval gating is not configured and execution may continue. Once a manager
// is configured this function fails closed: request/await errors and every
// non-approved terminal decision prevent the tool from running.
func AwaitToolApproval(
	ctx context.Context,
	manager ToolApprovalManager,
	input ToolApprovalRequest,
) (*model.ReviewRequest, error) {
	if manager == nil {
		return nil, nil
	}

	refID := strings.TrimSpace(input.RefID)
	if refID == "" {
		refID = "tool_" + strings.TrimPrefix(model.NewReviewID(), "rev_")
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = input.ToolName
	}

	r := model.NewReviewRequest("tool_call", refID)
	r.UserID = input.UserID
	r.SessionID = input.SessionID
	r.Title = "Approve tool: " + displayName
	r.Content = input.Arguments
	r.Metadata = map[string]any{
		"tool_name":  input.ToolName,
		"agent_type": string(input.AgentType),
	}

	reviewID, err := manager.Request(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("request tool approval: %w", err)
	}
	resolved, err := manager.Await(ctx, reviewID)
	if err != nil {
		return nil, fmt.Errorf("await tool approval %s: %w", reviewID, err)
	}
	if resolved == nil {
		return nil, fmt.Errorf("await tool approval %s: empty decision", reviewID)
	}
	if !resolved.IsApproved() {
		return resolved, &ToolApprovalRejectedError{
			ReviewID: resolved.ID,
			Decision: resolved.Decision,
			Note:     resolved.Note,
		}
	}
	return resolved, nil
}
