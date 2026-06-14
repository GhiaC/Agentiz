package model

// PromptSection is one labeled entry of the Core agent's assembled system-prompt
// array, surfaced for display in the debug UI.
//
// It is a transient *view* type — never persisted. On each routing turn the Core
// assembles an ordered array of system messages (see core.buildSystemPrompts);
// PromptSection captures one of those messages together with enough metadata to
// render and explain it on the user detail page: which section it is, whether it
// is required or optional, whether it varies per user (dynamic) or is stable per
// deployment (static), its size, and whether it actually made it into the array
// (optional sections can be dropped by the size budget).
//
// The type lives in model so both the builder (core) and the renderer (debuger)
// can share it without either package importing the other.
type PromptSection struct {
	// Key is a stable identifier for the section, e.g. "core_controller" or
	// "user_files". It matches the names used in the prompt-size metrics.
	Key string
	// Title is the human-readable heading shown in the UI.
	Title string
	// Content is the raw section text sent to the LLM as one system message.
	Content string
	// Required marks sections the Core cannot route without (controller, agent
	// descriptions, agent tools); these are always included, never dropped.
	Required bool
	// Dynamic marks sections that vary per user/turn (the user's memory, files,
	// sessions) as opposed to static sections that are byte-stable per deployment
	// and so cache provider-side.
	Dynamic bool
	// Included is false when the section was dropped by the size budget or is
	// empty, i.e. it is not part of the array actually sent to the LLM.
	Included bool
	// Bytes is len(Content).
	Bytes int
	// Note is an optional annotation, e.g. why a section is empty or unavailable
	// (used by the store-only preview for agent-dependent sections).
	Note string
}
