package model

import "time"

// User represents a user in the system
type User struct {
	// UserID is the unique identifier for the user
	UserID string

	// User information (optional)
	Name     string // User's display name (optional)
	Username string // User's username (optional)

	// Ban status
	IsBanned   bool      // Whether the user is currently banned
	BanUntil   time.Time // When the ban expires (zero time means permanent ban)
	BanMessage string    // Message to show to banned users

	// Nonsense message tracking
	NonsenseCount    int       // Number of consecutive nonsense messages
	LastNonsenseTime time.Time // Time of last nonsense message

	// Active session IDs per agent type
	// Key: AgentType (core, high, low), Value: SessionID
	// This is persisted to database and loaded on startup
	ActiveSessionIDs map[AgentType]string

	// Metadata
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewUser creates a new user
func NewUser(userID string) *User {
	now := time.Now()
	return &User{
		UserID:           userID,
		IsBanned:         false,
		BanUntil:         time.Time{},
		BanMessage:       "",
		NonsenseCount:    0,
		ActiveSessionIDs: make(map[AgentType]string),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// IsCurrentlyBanned checks if the user is currently banned
func (u *User) IsCurrentlyBanned() bool {
	if !u.IsBanned {
		return false
	}
	// If BanUntil is zero, it's a permanent ban
	if u.BanUntil.IsZero() {
		return true
	}
	// Check if ban has expired
	return time.Now().Before(u.BanUntil)
}

// Ban bans the user for a specified duration
// If duration is 0, it's a permanent ban
func (u *User) Ban(duration time.Duration, message string) {
	u.IsBanned = true
	if duration > 0 {
		u.BanUntil = time.Now().Add(duration)
	} else {
		u.BanUntil = time.Time{} // Zero time means permanent
	}
	u.BanMessage = message
	u.UpdatedAt = time.Now()
}

// Unban removes the ban from the user
func (u *User) Unban() {
	u.IsBanned = false
	u.BanUntil = time.Time{}
	u.BanMessage = ""
	u.NonsenseCount = 0
	u.UpdatedAt = time.Now()
}

// IncrementNonsenseCount increments the nonsense message count
func (u *User) IncrementNonsenseCount() {
	u.NonsenseCount++
	u.LastNonsenseTime = time.Now()
	u.UpdatedAt = time.Now()
}

// ResetNonsenseCount resets the nonsense message count
func (u *User) ResetNonsenseCount() {
	u.NonsenseCount = 0
	u.UpdatedAt = time.Now()
}

// GetActiveSessionID returns the active session ID for a given agent type
// Returns empty string if no active session exists
func (u *User) GetActiveSessionID(agentType AgentType) string {
	if u.ActiveSessionIDs == nil {
		return ""
	}
	return u.ActiveSessionIDs[agentType]
}

// SetActiveSessionID sets the active session ID for a given agent type
func (u *User) SetActiveSessionID(agentType AgentType, sessionID string) {
	if u.ActiveSessionIDs == nil {
		u.ActiveSessionIDs = make(map[AgentType]string)
	}
	u.ActiveSessionIDs[agentType] = sessionID
	u.UpdatedAt = time.Now()
}
