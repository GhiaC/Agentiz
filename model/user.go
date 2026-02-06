package model

import "time"

// User represents a user in the system
type User struct {
	// UserID is the unique identifier for the user
	UserID string

	// Ban status
	IsBanned   bool      // Whether the user is currently banned
	BanUntil   time.Time // When the ban expires (zero time means permanent ban)
	BanMessage string    // Message to show to banned users

	// Nonsense message tracking
	NonsenseCount    int       // Number of consecutive nonsense messages
	LastNonsenseTime time.Time // Time of last nonsense message

	// Metadata
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewUser creates a new user
func NewUser(userID string) *User {
	now := time.Now()
	return &User{
		UserID:        userID,
		IsBanned:      false,
		BanUntil:      time.Time{},
		BanMessage:    "",
		NonsenseCount: 0,
		CreatedAt:     now,
		UpdatedAt:     now,
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
