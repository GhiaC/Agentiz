package engine

import (
	"context"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
)

// UserModeration handles user ban and nonsense message detection
type UserModeration struct {
	// Nonsense detection functions
	isNonsenseFast func(string) bool
	isNonsenseLLM  func(context.Context, string) (bool, error)

	// User management functions
	getUser  func(string) (*model.User, error)
	saveUser func(*model.User) error
}

// NewUserModeration creates a new UserModeration helper
func NewUserModeration(
	isNonsenseFast func(string) bool,
	isNonsenseLLM func(context.Context, string) (bool, error),
	getUser func(string) (*model.User, error),
	saveUser func(*model.User) error,
) *UserModeration {
	return &UserModeration{
		isNonsenseFast: isNonsenseFast,
		isNonsenseLLM:  isNonsenseLLM,
		getUser:        getUser,
		saveUser:       saveUser,
	}
}

// CheckBanStatus checks if user is banned and returns ban message if applicable
func (um *UserModeration) CheckBanStatus(userID string) (isBanned bool, banMessage string) {
	user, err := um.getUser(userID)
	if err != nil {
		log.Log.Warnf("[UserModeration] ⚠️  Failed to get user | UserID: %s | Error: %v", userID, err)
		return false, ""
	}

	if user == nil || !user.IsCurrentlyBanned() {
		return false, ""
	}

	banMessage = user.BanMessage
	if banMessage == "" {
		banMessage = "You have been temporarily restricted due to irrelevant messages. Please try again later."
	}

	log.Log.Infof("[UserModeration] 🚫 User is banned | UserID: %s | BanUntil: %v", userID, user.BanUntil)
	return true, banMessage
}

// ProcessNonsenseCheck checks if message is nonsense and handles auto-ban logic
// Returns (shouldBan, banMessage, error)
func (um *UserModeration) ProcessNonsenseCheck(ctx context.Context, userID string, userMessage string) (shouldBan bool, banMessage string, err error) {
	user, err := um.getUser(userID)
	if err != nil {
		log.Log.Warnf("[UserModeration] ⚠️  Failed to get user | UserID: %s | Error: %v", userID, err)
		return false, "", err
	}

	if user == nil {
		return false, "", nil
	}

	// Fast check first
	isNonsense := um.isNonsenseFast(userMessage)

	// Use LLM verification if user has previous warnings
	if isNonsense && user.NonsenseCount > 0 {
		// Ensure user_id is in context for LLM call
		ctx = model.WithUserID(ctx, userID)
		llmNonsense, err := um.isNonsenseLLM(ctx, userMessage)
		if err != nil {
			log.Log.Warnf("[UserModeration] ⚠️  Failed to verify with LLM, using fast check result | Error: %v", err)
		} else {
			isNonsense = llmNonsense
		}
	} else if isNonsense {
		log.Log.Infof("[UserModeration] ⚠️  Fast check detected nonsense (first time) | UserID: %s", userID)
	}

	if !isNonsense {
		// Message is valid, reset nonsense count
		if user.NonsenseCount > 0 {
			user.ResetNonsenseCount()
			if err := um.saveUser(user); err != nil {
				log.Log.Warnf("[UserModeration] ⚠️  Failed to reset nonsense count | UserID: %s | Error: %v", userID, err)
			}
		}
		return false, "", nil
	}

	// Handle nonsense message
	user.IncrementNonsenseCount()
	log.Log.Infof("[UserModeration] ⚠️  Nonsense message detected | UserID: %s | Count: %d", userID, user.NonsenseCount)

	banDuration, banMessage := um.calculateBanDuration(user.NonsenseCount)

	if banDuration > 0 {
		user.Ban(banDuration, banMessage)
		if err := um.saveUser(user); err != nil {
			log.Log.Errorf("[UserModeration] ❌ Failed to save user ban | UserID: %s | Error: %v", userID, err)
			return false, "", err
		}
		log.Log.Infof("[UserModeration] 🚫 User auto-banned | UserID: %s | Duration: %v | Count: %d", userID, banDuration, user.NonsenseCount)
		return true, banMessage, nil
	}

	// Save updated nonsense count (warning only, no ban)
	if err := um.saveUser(user); err != nil {
		log.Log.Warnf("[UserModeration] ⚠️  Failed to save user | UserID: %s | Error: %v", userID, err)
	}
	return false, banMessage, nil
}

// calculateBanDuration calculates ban duration and message based on nonsense count
// Auto-ban thresholds: 3 messages = 1 hour, 5 messages = 6 hours, 7+ messages = 24 hours
func (um *UserModeration) calculateBanDuration(nonsenseCount int) (time.Duration, string) {
	switch {
	case nonsenseCount >= 7:
		return 24 * time.Hour, "You have been restricted for 24 hours due to repeated irrelevant messages."
	case nonsenseCount >= 5:
		return 6 * time.Hour, "You have been restricted for 6 hours due to repeated irrelevant messages."
	case nonsenseCount >= 3:
		return 1 * time.Hour, "You have been restricted for 1 hour due to repeated irrelevant messages."
	default:
		return 0, "Please send meaningful messages."
	}
}
