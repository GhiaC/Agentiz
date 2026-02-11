package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// SessionSchedulerConfig holds configuration for the session scheduler
type SessionSchedulerConfig struct {
	// CheckInterval is how often to check sessions (default: 5 minutes)
	CheckInterval time.Duration

	// FirstSummarizationThreshold is the minimum number of messages for FIRST summarization
	// when session has never been summarized before (default: 5)
	FirstSummarizationThreshold int

	// SubsequentMessageThreshold is the minimum number of messages for subsequent summarizations
	// after session has been summarized at least once (default: 25)
	SubsequentMessageThreshold int

	// SubsequentTimeThreshold is the minimum time that must pass after last summarization
	// before allowing another summarization (default: 1 hour)
	SubsequentTimeThreshold time.Duration

	// LastActivityThreshold is how recent LastActivity should be to consider session active (default: 1 hour)
	LastActivityThreshold time.Duration

	// ImmediateSummarizationThreshold is the message count that triggers immediate summarization
	// regardless of other conditions (default: 50)
	ImmediateSummarizationThreshold int

	// SummaryModel is the LLM model to use for summarization (default: gpt-4o-mini)
	SummaryModel string

	// DisableLogs if true, SessionScheduler does not emit any logs
	DisableLogs bool

	// SummarizationPrompts holds customizable prompts for summarization
	SummarizationPrompts SummarizationPrompts
}

// SummarizationPrompts holds customizable prompts for LLM-based summarization
type SummarizationPrompts struct {
	// SummarySystemPrompt is the system prompt for generating summaries
	// If empty, uses default prompt
	SummarySystemPrompt string

	// SummaryUserPromptTemplate is the user prompt template for summaries
	// Use {{.PreviousSummary}} and {{.ConversationText}} as placeholders
	// If empty, uses default template
	SummaryUserPromptTemplate string

	// TagSystemPrompt is the system prompt for generating tags
	TagSystemPrompt string

	// TagUserPromptTemplate is the user prompt template for tags
	// Use {{.ExistingTags}} and {{.ConversationText}} as placeholders
	TagUserPromptTemplate string

	// TitleSystemPrompt is the system prompt for generating titles
	TitleSystemPrompt string
}

// DefaultSessionSchedulerConfig returns default configuration
func DefaultSessionSchedulerConfig() SessionSchedulerConfig {
	return SessionSchedulerConfig{
		CheckInterval:                   5 * time.Minute,
		FirstSummarizationThreshold:     5,             // First summarization after 5 messages
		SubsequentMessageThreshold:      25,            // Subsequent summarizations need 25 messages
		SubsequentTimeThreshold:         1 * time.Hour, // Plus at least 1 hour since last summarization
		LastActivityThreshold:           1 * time.Hour, // Session must be active within last hour
		ImmediateSummarizationThreshold: 50,            // Immediate summarization when messages exceed 50
		SummarizationPrompts:            DefaultSummarizationPrompts(),
	}
}

// SummaryOffensiveContentSignal is the exact single word the LLM must return when user messages contain offensive/vulgar content. Used to trigger user ban.
const SummaryOffensiveContentSignal = "OFFENSIVE_CONTENT"

// userStore is implemented by stores that can get/save users (for ban on offensive content). Used via type assertion from session store.
type userStore interface {
	GetOrCreateUser(userID string) (*model.User, error)
	PutUser(user *model.User) error
}

// DefaultSummarizationPrompts returns default prompts for summarization
func DefaultSummarizationPrompts() SummarizationPrompts {
	return SummarizationPrompts{
		SummarySystemPrompt: `You are a conversation summarizer that extracts ONLY unique, specific, and personal information.

CONTENT VIOLATION (check first): If the user's messages contain offensive, vulgar, abusive, or clearly inappropriate language (insults, slurs, explicit content, hate speech, etc.), you MUST respond with ONLY this exact word, nothing else: OFFENSIVE_CONTENT. No explanation, no other text, no summary.

WHAT TO INCLUDE (specific/unique information):
- Names of people, places, or entities mentioned
- Personal details: age, birthday, preferences, relationships
- Specific decisions or commitments made
- Important numbers that define something (age, dates, IDs, specific values the user defined)
- Unique facts about the user or their situation
- Custom configurations or settings discussed

WHAT TO EXCLUDE (generic/common information):
- Greetings, pleasantries, and small talk
- General questions and answers (unit conversions, weather, time, etc.)
- Temporary calculations or arithmetic results
- Generic how-to questions
- Common knowledge lookups
- Filler content and acknowledgments

Requirements:
- Maximum 200 characters
- Only include information that would be LOST if not summarized
- If nothing specific/unique was discussed, return empty or minimal summary
- If a previous summary is provided, preserve its specific information and add new specifics

Example GOOD summary: "User Ali, 28 years old, prefers dark mode. Works at TechCorp on Kubernetes projects."
Example BAD summary: "Discussed unit conversions and weather. User asked about time zones."`,

		SummaryUserPromptTemplate: `{{if .PreviousSummary}}Previous summary (preserve specific info): {{.PreviousSummary}}

New conversation to incorporate:
{{end}}{{.ConversationText}}

Extract ONLY specific/unique information (names, ages, personal details, important decisions). Ignore generic questions and small talk:`,

		TagSystemPrompt: `You are a conversation tagger that identifies SPECIFIC topics only.

WHAT TO TAG (specific topics):
- Named entities (people, companies, products)
- Technical projects or systems being worked on
- Personal topics (health, family, work)
- Specific domains the user is interested in

WHAT NOT TO TAG (generic topics):
- "questions", "help", "conversation"
- "math", "calculations", "conversions" (unless it's a recurring theme)
- "greetings", "chat"
- Any generic action words

CRITICAL RULES:
1. PRESERVE existing tags that are still relevant
2. ADD new tags only for genuinely specific new topics
3. Maximum 7 tags total
4. Be very selective - fewer specific tags are better than many generic ones

Format: lowercase, hyphenated (e.g., "kubernetes", "project-alpha", "user-preferences")
Return only the final tag list, comma-separated, no quotes or extra text.`,

		TagUserPromptTemplate: `{{if .ExistingTags}}EXISTING TAGS (preserve these unless completely irrelevant): {{.ExistingTags}}

{{end}}NEW CONVERSATION CONTENT:
{{.ConversationText}}

Generate tags for SPECIFIC topics only (ignore generic questions and small talk):`,

		TitleSystemPrompt: `Generate a short title (3-5 words) for this conversation.
Focus on the SPECIFIC topic or person discussed, not generic actions.

Good examples:
- "Ali's Kubernetes Project"
- "TechCorp API Design"
- "User Profile Settings"

Bad examples (too generic):
- "Questions and Answers"
- "Help with Calculations"
- "General Discussion"

Return only the title, no quotes or extra text.`,
	}
}

// SessionScheduler periodically checks and summarizes sessions
type SessionScheduler struct {
	sessionHandler *model.SessionHandler
	llmClient      *openai.Client
	backups        *backupChain // backup LLM providers (OSS 120B first, then others)
	config         SessionSchedulerConfig
	stopChan       chan struct{}
	running        bool
	mu             sync.Mutex
}

// NewSessionScheduler creates a new session scheduler
func NewSessionScheduler(
	sessionHandler *model.SessionHandler,
	llmClient *openai.Client,
	config SessionSchedulerConfig,
) *SessionScheduler {
	return &SessionScheduler{
		sessionHandler: sessionHandler,
		llmClient:      llmClient,
		config:         config,
		stopChan:       make(chan struct{}),
		running:        false,
	}
}

// SetBackupChain sets the backup LLM chain for the scheduler
// Backups are tried IN ORDER before falling back to the main llmClient
// Recommended: put OSS 120B first for cost efficiency
func (ss *SessionScheduler) SetBackupChain(backups *backupChain) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.backups = backups
}

// Start starts the scheduler in a background goroutine
func (ss *SessionScheduler) Start(ctx context.Context) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if ss.running {
		if !ss.config.DisableLogs {
			log.Log.Warnf("[SessionScheduler] ⚠️  Scheduler is already running")
		}
		return
	}

	ss.running = true
	ss.stopChan = make(chan struct{}) // Recreate stopChan in case it was closed
	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🚀 Starting session scheduler | CheckInterval: %v | FirstThreshold: %d msgs | SubsequentThreshold: %d msgs + %v",
			ss.config.CheckInterval, ss.config.FirstSummarizationThreshold, ss.config.SubsequentMessageThreshold, ss.config.SubsequentTimeThreshold)
	}

	go ss.run(ctx)
}

// Stop stops the scheduler gracefully
func (ss *SessionScheduler) Stop() {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if !ss.running {
		return
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🛑 Stopping session scheduler")
	}
	close(ss.stopChan)
	ss.running = false
}

// GetMessageThreshold returns the first summarization threshold from scheduler config
// This is used for backward compatibility
func (ss *SessionScheduler) GetMessageThreshold() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.config.FirstSummarizationThreshold
}

// GetConfig returns the full scheduler configuration
func (ss *SessionScheduler) GetConfig() SessionSchedulerConfig {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.config
}

// isStopping checks if shutdown has been requested
func (ss *SessionScheduler) isStopping() bool {
	select {
	case <-ss.stopChan:
		return true
	default:
		return false
	}
}

// sleepWithCancel sleeps for the given duration but can be interrupted by stop signal
func (ss *SessionScheduler) sleepWithCancel(d time.Duration) bool {
	select {
	case <-time.After(d):
		return false // completed normally
	case <-ss.stopChan:
		return true // interrupted
	}
}

// chatCompletion tries backup providers first (OSS 120B priority), then falls back to main llmClient
// This optimizes for cost by using cheaper models for summarization tasks
func (ss *SessionScheduler) chatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// Try backup chain first (OSS 120B should be first in the chain for priority)
	if ss.backups != nil {
		log.Log.Infof("[SessionScheduler] 🔄 BACKUP CHAIN >> Attempting backup chain for summarization | BackupProviders: %d | RequestModel: %s",
			len(ss.backups.providers), request.Model)
		resp, ok := ss.backups.tryBackup(ctx, request.Messages, nil, "SessionScheduler")
		if ok {
			log.Log.Infof("[SessionScheduler] ✅ BACKUP CHAIN >> Success | UsedModel: %s | ResponseTokens: %d",
				resp.Model, resp.Usage.TotalTokens)
			return resp, nil
		}
		log.Log.Warnf("[SessionScheduler] ⚠️ BACKUP CHAIN >> All backup providers failed, falling back to main LLM: %s", ss.config.SummaryModel)
	} else {
		log.Log.Warnf("[SessionScheduler] ⚠️ BACKUP CHAIN >> No backup chain configured, using main LLM: %s", ss.config.SummaryModel)
	}

	// Fall back to main llmClient
	log.Log.Infof("[SessionScheduler] 🔵 MAIN LLM >> Calling main LLM | Model: %s", ss.config.SummaryModel)
	return ss.llmClient.CreateChatCompletion(ctx, request)
}

// run runs the scheduler loop
func (ss *SessionScheduler) run(ctx context.Context) {
	// Check for early shutdown before initial check
	if ss.isStopping() || ctx.Err() != nil {
		if !ss.config.DisableLogs {
			log.Log.Infof("[SessionScheduler] ✅ Scheduler stopped before initial check")
		}
		return
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🔍 Starting initial session check (checking all sessions immediately)...")
	}
	ss.checkAndSummarizeSessions(ctx)

	// Check for shutdown after initial check
	if ss.isStopping() || ctx.Err() != nil {
		if !ss.config.DisableLogs {
			log.Log.Infof("[SessionScheduler] ✅ Scheduler stopped after initial check")
		}
		return
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] ✅ Initial session check completed, starting periodic checks...")
	}

	ticker := time.NewTicker(ss.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ss.checkAndSummarizeSessions(ctx)
		case <-ss.stopChan:
			if !ss.config.DisableLogs {
				log.Log.Infof("[SessionScheduler] ✅ Scheduler stopped")
			}
			return
		case <-ctx.Done():
			if !ss.config.DisableLogs {
				log.Log.Infof("[SessionScheduler] ✅ Scheduler stopped (context cancelled)")
			}
			return
		}
	}
}

// checkAndSummarizeSessions checks all sessions and summarizes eligible ones
func (ss *SessionScheduler) checkAndSummarizeSessions(ctx context.Context) {
	// Check for early shutdown
	if ss.isStopping() || ctx.Err() != nil {
		return
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🔍 Checking sessions for summarization...")
	}

	// Get all sessions from store
	sessionStore := ss.sessionHandler.GetStore()
	debugStore, ok := sessionStore.(debuger.DebugStore)
	if !ok {
		if !ss.config.DisableLogs {
			log.Log.Errorf("[SessionScheduler] ❌ Store does not implement DebugStore interface")
		}
		return
	}

	sessionsByUser, err := debugStore.GetAllSessions()
	if err != nil {
		if !ss.config.DisableLogs {
			log.Log.Errorf("[SessionScheduler] ❌ Failed to get all sessions: %v", err)
		}
		return
	}

	totalSessions := 0
	eligibleSessions := 0
	summarizedSessions := 0
	sessionsWithMessages := 0
	sessionsWithoutMessages := 0
	alreadySummarizedSessions := 0
	sessionsNotEligible := 0
	totalUsers := len(sessionsByUser)
	totalMessages := 0
	stoppedEarly := false

	now := time.Now()

	// Iterate through all sessions
sessionLoop:
	for userID, sessions := range sessionsByUser {
		totalSessions += len(sessions)
		for _, session := range sessions {
			// Check for shutdown before processing each session
			if ss.isStopping() || ctx.Err() != nil {
				if !ss.config.DisableLogs {
					log.Log.Infof("[SessionScheduler] 🛑 Shutdown requested, stopping session check early")
				}
				stoppedEarly = true
				break sessionLoop
			}

			msgCount := len(session.Msgs)
			totalMessages += msgCount

			// Track sessions with/without messages
			if msgCount > 0 {
				sessionsWithMessages++
			} else {
				sessionsWithoutMessages++
			}

			// Track already summarized sessions
			if !session.SummarizedAt.IsZero() {
				alreadySummarizedSessions++
			}

			isEligible := ss.isEligibleForSummarization(session, now)
			if !isEligible && msgCount > 0 {
				sessionsNotEligible++
				// Log why session is not eligible (only for sessions with messages)
				reasons := []string{}
				if msgCount == 0 {
					reasons = append(reasons, "no messages")
				}

				// Check thresholds based on whether session was summarized before
				if session.SummarizedAt.IsZero() {
					// First summarization check
					if msgCount < ss.config.FirstSummarizationThreshold {
						reasons = append(reasons, fmt.Sprintf("only %d messages (need %d for first summarization)", msgCount, ss.config.FirstSummarizationThreshold))
					}
				} else {
					// Subsequent summarization check
					if msgCount < ss.config.SubsequentMessageThreshold {
						reasons = append(reasons, fmt.Sprintf("only %d messages (need %d for subsequent summarization)", msgCount, ss.config.SubsequentMessageThreshold))
					}
					summarizedAge := now.Sub(session.SummarizedAt)
					if summarizedAge < ss.config.SubsequentTimeThreshold {
						reasons = append(reasons, fmt.Sprintf("summarized %v ago (need %v)", summarizedAge, ss.config.SubsequentTimeThreshold))
					}
					if !session.UpdatedAt.IsZero() {
						lastActivityAge := now.Sub(session.UpdatedAt)
						if lastActivityAge > ss.config.LastActivityThreshold {
							reasons = append(reasons, fmt.Sprintf("last activity %v ago (need within %v)", lastActivityAge, ss.config.LastActivityThreshold))
						}
					}
				}
				if len(reasons) > 0 && !ss.config.DisableLogs {
					log.Log.Debugf("[SessionScheduler] ⏭️  Session %s not eligible: %s | Messages: %d", session.SessionID, strings.Join(reasons, ", "), msgCount)
				}
			}
			if isEligible {
				eligibleSessions++
				if !ss.config.DisableLogs {
					log.Log.Infof("[SessionScheduler] 🎯 Session eligible for summarization | SessionID: %s | UserID: %s | Messages: %d", session.SessionID, userID, msgCount)
				}
				if err := ss.summarizeSession(ctx, session); err != nil {
					// Check if error is due to context cancellation
					if ctx.Err() != nil {
						if !ss.config.DisableLogs {
							log.Log.Infof("[SessionScheduler] 🛑 Summarization cancelled due to shutdown")
						}
						stoppedEarly = true
						break sessionLoop
					}
					if !ss.config.DisableLogs {
						log.Log.Errorf("[SessionScheduler] ❌ Failed to summarize session %s: %v", session.SessionID, err)
					}
				} else {
					summarizedSessions++
					if !ss.config.DisableLogs {
						log.Log.Infof("[SessionScheduler] ✅ Summarized session %s (UserID: %s)", session.SessionID, userID)
					}
				}

				// Sleep with cancellation support
				if !ss.config.DisableLogs {
					log.Log.Infof("[SessionScheduler] ⏸️  Sleeping 10 seconds before next summarization...")
				}
				if ss.sleepWithCancel(10 * time.Second) {
					if !ss.config.DisableLogs {
						log.Log.Infof("[SessionScheduler] 🛑 Sleep interrupted by shutdown")
					}
					stoppedEarly = true
					break sessionLoop
				}
			}
		}
	}

	if !ss.config.DisableLogs {
		status := "completed"
		if stoppedEarly {
			status = "interrupted by shutdown"
		}
		log.Log.Infof("[SessionScheduler] 📊 Summary check %s | Total: %d | Users: %d | Messages: %d | WithMsgs: %d | NoMsgs: %d | AlreadySummarized: %d | NotEligible: %d | Eligible: %d | Summarized: %d | FirstThreshold: %d msgs | SubsequentThreshold: %d msgs + %v time",
			status, totalSessions, totalUsers, totalMessages, sessionsWithMessages, sessionsWithoutMessages, alreadySummarizedSessions, sessionsNotEligible, eligibleSessions, summarizedSessions,
			ss.config.FirstSummarizationThreshold, ss.config.SubsequentMessageThreshold, ss.config.SubsequentTimeThreshold)
	}
}

// isEligibleForSummarization checks if a session is eligible for summarization
// Three different thresholds apply:
// 1. Immediate summarization: if messages >= ImmediateSummarizationThreshold (default: 50), summarize immediately
// 2. First summarization (never summarized): only needs FirstSummarizationThreshold messages
// 3. Subsequent summarizations: needs SubsequentMessageThreshold messages AND SubsequentTimeThreshold time since last summarization
func (ss *SessionScheduler) isEligibleForSummarization(session *model.Session, now time.Time) bool {
	// Check if session has messages
	if len(session.Msgs) == 0 {
		return false
	}

	msgCount := len(session.Msgs)

	// IMMEDIATE CASE: If messages exceed ImmediateSummarizationThreshold, summarize immediately
	// This overrides all other conditions to prevent context window overflow
	immediateThreshold := ss.config.ImmediateSummarizationThreshold
	if immediateThreshold <= 0 {
		immediateThreshold = 50 // fallback default
	}
	if msgCount >= immediateThreshold {
		if !ss.config.DisableLogs {
			log.Log.Infof("[SessionScheduler] ⚡ Immediate summarization triggered for session (messages: %d >= threshold: %d)",
				msgCount, immediateThreshold)
		}
		return true
	}

	// CASE 1: First summarization (session never summarized before)
	if session.SummarizedAt.IsZero() {
		// Only need FirstSummarizationThreshold messages (default: 5)
		return msgCount >= ss.config.FirstSummarizationThreshold
	}

	// CASE 2: Subsequent summarization (session has been summarized before)
	// Need BOTH conditions:
	// - At least SubsequentMessageThreshold messages (default: 25)
	// - At least SubsequentTimeThreshold time since last summarization (default: 1 hour)

	// Check message threshold
	if msgCount < ss.config.SubsequentMessageThreshold {
		return false
	}

	// Check time threshold since last summarization
	timeSinceLastSummarization := now.Sub(session.SummarizedAt)
	if timeSinceLastSummarization < ss.config.SubsequentTimeThreshold {
		return false
	}

	// Check if LastActivity is recent (session is being used)
	if session.UpdatedAt.IsZero() {
		return false
	}
	lastActivityRecent := now.Sub(session.UpdatedAt) <= ss.config.LastActivityThreshold
	if !lastActivityRecent {
		return false
	}

	return true
}

// summarizeSession summarizes a session and moves messages to ArchivedMsgs
func (ss *SessionScheduler) summarizeSession(ctx context.Context, session *model.Session) error {
	// Lock the session to prevent race conditions with AddMessage
	ss.sessionHandler.LockSession(session.SessionID)
	defer ss.sessionHandler.UnlockSession(session.SessionID)

	// Re-fetch the session to get latest state after acquiring lock
	sessionStore := ss.sessionHandler.GetStore()
	freshSession, err := sessionStore.Get(session.SessionID)
	if err != nil {
		return fmt.Errorf("failed to get fresh session: %w", err)
	}
	session = freshSession

	// TODO Remove this
	// Repair: if session has SummarizedAt but Summary is empty, try to restore from latest summarization log
	//if !session.SummarizedAt.IsZero() && session.Summary == "" {
	//	if debugStore, ok := sessionStore.(debuger.DebugStore); ok {
	//		logs, err := debugStore.GetSummarizationLogsBySession(session.SessionID)
	//		if err == nil {
	//			for _, l := range logs {
	//				if l.GeneratedSummary != "" {
	//					session.Summary = l.GeneratedSummary
	//					if err := sessionStore.Put(session); err == nil && !ss.config.DisableLogs {
	//						log.Log.Infof("[SessionScheduler] 🔧 Repaired session %s: restored Summary from summarization log", session.SessionID)
	//					}
	//					return nil
	//				}
	//			}
	//		}
	//	}
	//}

	msgCount := len(session.Msgs)
	// When Msgs is empty but summarization is needed (e.g. SummarizedAt set but Summary empty), use ArchivedMsgs
	useArchivedForSummary := false
	if msgCount == 0 {
		if len(session.ArchivedMsgs) == 0 {
			if !ss.config.DisableLogs {
				log.Log.Infof("[SessionScheduler] ⏭️  Session %s has no messages and no archived messages, skipping", session.SessionID)
			}
			return nil
		}
		// Should re-summarize using archived messages (e.g. incomplete: SummarizedAt set but Summary empty)
		useArchivedForSummary = true
		msgCount = len(session.ArchivedMsgs) // for log stats
		if !ss.config.DisableLogs {
			log.Log.Infof("[SessionScheduler] 📝 Session %s has no current Msgs, using %d archived messages for summarization", session.SessionID, msgCount)
		}
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 📝 Summarizing session %s | Messages: %d | PreviousSummary: %s | ExistingTags: %v",
			session.SessionID, msgCount, truncateStringForLog(session.Summary, 50), session.Tags)
	}

	// Ensure user_id is in context
	ctx = model.WithUserID(ctx, session.UserID)

	// Determine summarization type
	summarizationType := "first"
	if !session.SummarizedAt.IsZero() {
		if msgCount >= ss.config.ImmediateSummarizationThreshold {
			summarizationType = "immediate"
		} else {
			summarizationType = "subsequent"
		}
	}

	// Create summarization log with all context before summarization
	summLog := model.NewSummarizationLog(session)
	summLog.SessionTitle = session.Title
	summLog.PreviousSummary = session.Summary
	summLog.PreviousTags = strings.Join(session.Tags, ", ")
	summLog.MessagesBeforeCount = msgCount
	summLog.ArchivedMessagesCount = len(session.ArchivedMsgs)
	summLog.RequestedModel = ss.config.SummaryModel
	summLog.SummarizationType = summarizationType

	// Get debug store for logging
	debugStore, hasDebugStore := sessionStore.(debuger.DebugStore)

	// Format conversation for summarization (use current Msgs or ArchivedMsgs when Msgs empty)
	var conversationText string
	if useArchivedForSummary {
		conversationText = formatMessagesForSummary(session.ArchivedMsgs)
	} else {
		conversationText = formatMessagesForSummary(session.Msgs)
	}

	// Track what we generate
	var generatedSummary, generatedTags, generatedTitle string

	// Generate improved summary (incorporating previous summary)
	previousSummary := session.Summary
	newSummary, summaryResp, promptSent, err := ss.generateImprovedSummaryWithResponse(ctx, session.SessionID, session.UserID, previousSummary, conversationText)
	summLog.PromptSent = promptSent // Store prompt for debug/DB (even on failure)
	if err != nil {
		if !ss.config.DisableLogs {
			log.Log.Warnf("[SessionScheduler] ⚠️  Failed to generate summary for session %s: %v", session.SessionID, err)
		}
		summLog.ErrorMessage = fmt.Sprintf("summary generation failed: %v", err)
		summLog.MarkCompleted("failed")
		if hasDebugStore {
			_ = debugStore.PutSummarizationLog(summLog)
		}
		// Do not set SummarizedAt or move messages when summary generation failed
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	// Offensive content: LLM returned the signal word → ban user and do not save summary
	if strings.TrimSpace(newSummary) == SummaryOffensiveContentSignal {
		if us, ok := sessionStore.(userStore); ok {
			user, err := us.GetOrCreateUser(session.UserID)
			if err == nil {
				user.Ban(0, "شما به دلیل استفاده از عبارات نامناسب محدود شده‌اید.")
				if putErr := us.PutUser(user); putErr == nil && !ss.config.DisableLogs {
					log.Log.Infof("[SessionScheduler] 🚫 User banned (offensive content) | UserID: %s", session.UserID)
				}
			}
		}
		// Do not save OFFENSIVE_CONTENT as summary; do not set SummarizedAt or move messages
		return nil
	}

	// Always assign so persisted state reflects LLM response (even when empty)
	session.Summary = newSummary
	generatedSummary = newSummary
	if summaryResp != nil {
		summLog.PromptTokens = summaryResp.Usage.PromptTokens
		summLog.CompletionTokens = summaryResp.Usage.CompletionTokens
		summLog.TotalTokens = summaryResp.Usage.TotalTokens
		if summaryResp.Model != "" {
			summLog.ModelUsed = summaryResp.Model
		}
	}

	// Generate and merge tags
	existingTags := session.Tags
	newTags, err := ss.generateAndMergeTags(ctx, existingTags, conversationText)
	if err != nil {
		if !ss.config.DisableLogs {
			log.Log.Warnf("[SessionScheduler] ⚠️  Failed to generate tags for session %s: %v", session.SessionID, err)
		}
	} else if len(newTags) > 0 {
		session.Tags = newTags
		generatedTags = strings.Join(newTags, ", ")
	}

	// Generate title if not set
	if session.Title == "" {
		title, err := ss.generateTitle(ctx, conversationText)
		if err != nil {
			if !ss.config.DisableLogs {
				log.Log.Warnf("[SessionScheduler] ⚠️  Failed to generate title for session %s: %v", session.SessionID, err)
			}
		} else if title != "" {
			session.Title = title
			generatedTitle = title
		}
	}

	// Update log with generated content
	summLog.GeneratedSummary = generatedSummary
	summLog.GeneratedTags = generatedTags
	summLog.GeneratedTitle = generatedTitle
	summLog.ResponseReceived = generatedSummary
	if summLog.ModelUsed == "" {
		summLog.ModelUsed = ss.config.SummaryModel
	}

	// When we had current Msgs: move them to ArchivedMsgs. When we used archived only: no move.
	msgsToMove := make([]openai.ChatCompletionMessage, len(session.Msgs))
	copy(msgsToMove, session.Msgs)

	var archivedMsgsBackupLen int
	previousSummarizedAt := session.SummarizedAt
	if len(msgsToMove) > 0 {
		archivedMsgsBackupLen = len(session.ArchivedMsgs)
		session.ArchivedMsgs = append(session.ArchivedMsgs, msgsToMove...)
		session.Msgs = []openai.ChatCompletionMessage{}
	}

	session.SummarizedAt = time.Now()
	session.UpdatedAt = time.Now()

	// Update log with after-state before save
	summLog.MessagesAfterCount = len(session.Msgs)
	summLog.ArchivedMessagesCount = len(session.ArchivedMsgs)

	// Save session - if this fails, rollback all in-memory changes
	if err := sessionStore.Put(session); err != nil {
		if len(msgsToMove) > 0 {
			session.Msgs = msgsToMove
			session.ArchivedMsgs = session.ArchivedMsgs[:archivedMsgsBackupLen]
		}
		session.Summary = previousSummary
		session.SummarizedAt = previousSummarizedAt
		summLog.MarkCompleted("failed")
		summLog.ErrorMessage = fmt.Sprintf("failed to save session: %v", err)
		if hasDebugStore {
			_ = debugStore.PutSummarizationLog(summLog)
		}
		return fmt.Errorf("failed to save session: %w", err)
	}

	// Mark log as successful and save
	summLog.MarkCompleted("success")
	if hasDebugStore {
		_ = debugStore.PutSummarizationLog(summLog)
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] ✅ Session %s summarized | Type: %s | Moved: %d msgs | Archived: %d | Summary: %s | Tags: %v | Duration: %dms",
			session.SessionID, summarizationType, len(msgsToMove), len(session.ArchivedMsgs),
			truncateStringForLog(session.Summary, 50), session.Tags, summLog.DurationMs)
	}

	return nil
}

// generateImprovedSummaryWithResponse generates an improved summary and returns the full response and the prompt sent (for logging).
func (ss *SessionScheduler) generateImprovedSummaryWithResponse(ctx context.Context, sessionID string, userID string, previousSummary string, conversationText string) (string, *openai.ChatCompletionResponse, string, error) {
	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🔍 generateImprovedSummaryWithResponse called | SessionID: %s | PreviousSummary: %s",
			sessionID, truncateStringForLog(previousSummary, 50))
	}

	// Use configured prompts
	systemPrompt := ss.config.SummarizationPrompts.SummarySystemPrompt
	if systemPrompt == "" {
		systemPrompt = DefaultSummarizationPrompts().SummarySystemPrompt
	}

	// Build user prompt from template
	userPromptTemplate := ss.config.SummarizationPrompts.SummaryUserPromptTemplate
	if userPromptTemplate == "" {
		userPromptTemplate = DefaultSummarizationPrompts().SummaryUserPromptTemplate
	}

	userPrompt := userPromptTemplate
	if previousSummary != "" {
		userPrompt = strings.Replace(userPrompt, "{{if .PreviousSummary}}", "", 1)
		userPrompt = strings.Replace(userPrompt, "{{end}}", "", 1)
		userPrompt = strings.Replace(userPrompt, "{{.PreviousSummary}}", previousSummary, 1)
	} else {
		// Remove the conditional block if no previous summary
		startIdx := strings.Index(userPrompt, "{{if .PreviousSummary}}")
		endIdx := strings.Index(userPrompt, "{{end}}")
		if startIdx != -1 && endIdx != -1 {
			userPrompt = userPrompt[:startIdx] + userPrompt[endIdx+7:]
		}
	}
	userPrompt = strings.Replace(userPrompt, "{{.ConversationText}}", conversationText, 1)

	// Build prompt string for DB/debug (same format as sent to LLM)
	promptSent := formatPromptForLog(systemPrompt, userPrompt)

	request := openai.ChatCompletionRequest{
		Model: ss.config.SummaryModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt},
		},
		MaxTokens: 1000,
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🔵 LLM >> Model: %s | Messages: %d (improved summary)", ss.config.SummaryModel, len(request.Messages))
	}

	resp, err := ss.chatCompletion(ctx, request)
	if err != nil {
		return "", nil, promptSent, err
	}

	if len(resp.Choices) == 0 {
		return "", nil, promptSent, fmt.Errorf("no response from LLM")
	}

	summary := getMessageContentString(resp.Choices[0].Message)

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 📊 TOKEN USAGE >> total=%d (improved summary)", resp.Usage.TotalTokens)
	}

	return summary, &resp, promptSent, nil
}

// formatPromptForLog formats system and user prompt for storage in SummarizationLog.PromptSent
func formatPromptForLog(systemPrompt, userPrompt string) string {
	return "=== System ===\n" + systemPrompt + "\n\n=== User ===\n" + userPrompt
}

// generateAndMergeTags generates new tags and merges with existing ones
func (ss *SessionScheduler) generateAndMergeTags(ctx context.Context, existingTags []string, conversationText string) ([]string, error) {
	// Use configured prompts
	systemPrompt := ss.config.SummarizationPrompts.TagSystemPrompt
	if systemPrompt == "" {
		systemPrompt = DefaultSummarizationPrompts().TagSystemPrompt
	}

	userPromptTemplate := ss.config.SummarizationPrompts.TagUserPromptTemplate
	if userPromptTemplate == "" {
		userPromptTemplate = DefaultSummarizationPrompts().TagUserPromptTemplate
	}

	userPrompt := userPromptTemplate
	if len(existingTags) > 0 {
		userPrompt = strings.Replace(userPrompt, "{{if .ExistingTags}}", "", 1)
		userPrompt = strings.Replace(userPrompt, "{{end}}", "", 1)
		userPrompt = strings.Replace(userPrompt, "{{.ExistingTags}}", strings.Join(existingTags, ", "), 1)
	} else {
		startIdx := strings.Index(userPrompt, "{{if .ExistingTags}}")
		endIdx := strings.Index(userPrompt, "{{end}}")
		if startIdx != -1 && endIdx != -1 {
			userPrompt = userPrompt[:startIdx] + userPrompt[endIdx+7:]
		}
	}
	userPrompt = strings.Replace(userPrompt, "{{.ConversationText}}", conversationText, 1)

	request := openai.ChatCompletionRequest{
		Model: ss.config.SummaryModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt},
		},
		MaxTokens: 50,
	}

	resp, err := ss.chatCompletion(ctx, request)
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	// Parse tags from response - LLM handles merging intelligently via prompt
	tagsStr := getMessageContentString(resp.Choices[0].Message)
	tagsStr = strings.Trim(tagsStr, "\"'")
	rawTags := strings.Split(tagsStr, ",")

	// Clean and normalize tags
	result := make([]string, 0, len(rawTags))
	seen := make(map[string]bool)

	for _, tag := range rawTags {
		tag = strings.TrimSpace(strings.ToLower(tag))
		if tag != "" && !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}

	// Sort for consistency
	sort.Strings(result)

	return result, nil
}

// generateTitle generates a title for the session
func (ss *SessionScheduler) generateTitle(ctx context.Context, conversationText string) (string, error) {
	systemPrompt := ss.config.SummarizationPrompts.TitleSystemPrompt
	if systemPrompt == "" {
		systemPrompt = DefaultSummarizationPrompts().TitleSystemPrompt
	}

	// Truncate conversation if too long
	if len(conversationText) > 300 {
		conversationText = conversationText[:300] + "..."
	}

	request := openai.ChatCompletionRequest{
		Model: ss.config.SummaryModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Generate a title for this conversation:\n\n" + conversationText},
		},
		MaxTokens: 20,
	}

	resp, err := ss.chatCompletion(ctx, request)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return getMessageContentString(resp.Choices[0].Message), nil
}

// getMessageContentString returns the text content of a chat message as a trimmed string.
// Use this when reading summary/tags/title from LLM responses so parsing can be extended
// if the SDK or provider uses content parts (e.g. array) instead of a plain string.
func getMessageContentString(msg openai.ChatCompletionMessage) string {
	return strings.TrimSpace(msg.Content)
}

// formatMessagesForSummary converts messages to a readable format for summarization
func formatMessagesForSummary(msgs []openai.ChatCompletionMessage) string {
	var result string
	for _, msg := range msgs {
		// Only include user messages for summarization
		if msg.Role != openai.ChatMessageRoleUser {
			continue
		}
		// Skip tool-related messages
		if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 {
			continue
		}

		content := getMessageContentString(msg)
		if content == "" {
			continue
		}

		// Truncate long messages
		if len(content) > 300 {
			content = content[:300] + "..."
		}

		result += fmt.Sprintf("%s: %s\n", msg.Role, content)
	}
	return result
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// truncateStringForLog truncates a string to a maximum length for logging
func truncateStringForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// OpenAIClientWrapper wraps openai.Client to implement model.LLMClient interface
type OpenAIClientWrapper struct {
	Client *openai.Client
}

// CreateChatCompletion implements model.LLMClient interface
func (w *OpenAIClientWrapper) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return w.Client.CreateChatCompletion(ctx, request)
}
