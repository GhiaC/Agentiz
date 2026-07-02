package engine

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// ============================================================================
// Text-inspection tools for large/buffered tool results
// ----------------------------------------------------------------------------
// When a tool result exceeds MaxToolResultLength it is buffered on the session
// under a result_id (see processToolResult) instead of being handed to the
// model whole. These tools let the model pull back just what it needs:
//
//   - collect_result : LLM-backed semantic extraction ("what's the error?").
//   - inspect_result : deterministic, no-LLM slicing — stats, head, tail,
//                       slice (line range), and grep (regex + options).
//
// SECURITY — per-user isolation (do not weaken):
// Every access goes through getOwnedToolResult, which binds retrieval to the
// caller's OWN user + session as injected by executeTool (__user_id__ /
// __session_id__) — values the model cannot set. The result_id is supplied by
// the model and is therefore UNTRUSTED: we verify it names the caller's own
// session and that the owning session's UserID matches the caller before any
// buffered bytes are returned. A user can never read, search, or summarize
// another user's buffered output by guessing or replaying a foreign result_id.
// ============================================================================

const (
	// maxInspectResultChars caps the size of any inspect_result response so a
	// slice of a huge buffer can't re-explode the context. Larger than the
	// tiny collect_result budget because head/tail 30 lines is the norm.
	maxInspectResultChars = 4000
	// defaultInspectLines is the default window for head/tail.
	defaultInspectLines = 30
	// maxInspectLines caps how many lines head/tail/slice will emit at once.
	maxInspectLines = 500
	// maxInspectGrepMatches caps grep output so a broad pattern can't dump it all.
	maxInspectGrepMatches = 200
	// maxGrepContext caps the -C context window a single grep may request.
	maxGrepContext = 10
)

// getOwnedToolResult retrieves a buffered tool result, enforcing that it
// belongs to the caller. callerUserID/callerSessionID come from executeTool
// (injected from the authenticated session, never the model); resultID is
// model-supplied and untrusted. See the security note at the top of this file.
func (e *Engine) getOwnedToolResult(callerUserID, callerSessionID, resultID string) (string, error) {
	resultID = strings.TrimSpace(resultID)
	if resultID == "" {
		return "", fmt.Errorf("result_id is required")
	}
	// The caller identity is injected by executeTool from the authenticated
	// session and must be present — without it we cannot establish ownership, so
	// we refuse rather than fall back to a weaker check.
	if strings.TrimSpace(callerSessionID) == "" {
		return "", fmt.Errorf("access denied: no session context")
	}

	// The result_id embeds the session that produced it. Verify that session is
	// the caller's own before touching any stored data.
	owningSession, ok := parseResultID(resultID)
	if !ok {
		return "", fmt.Errorf("invalid result_id format: %q", resultID)
	}
	if owningSession != callerSessionID {
		return "", fmt.Errorf("access denied: result_id %q does not belong to this session", resultID)
	}

	session, err := e.Sessions.Get(owningSession)
	if err != nil {
		return "", fmt.Errorf("result with ID %q not found", resultID)
	}

	// Defense in depth: buffered output is owned by a user; only its owner may
	// read it. Exact match (both empty in single-tenant / no-auth setups).
	if strings.TrimSpace(session.UserID) != strings.TrimSpace(callerUserID) {
		return "", fmt.Errorf("access denied: result_id %q does not belong to you", resultID)
	}

	if session.ToolResults == nil {
		return "", fmt.Errorf("result with ID %q not found", resultID)
	}
	full, ok := session.ToolResults[resultID]
	if !ok {
		return "", fmt.Errorf("result with ID %q not found", resultID)
	}
	return full, nil
}

// RegisterTextTools registers the collect_result and inspect_result functions on
// the engine's function registry. It uses RegisterOrReplace so the ownership-
// enforcing library implementations are authoritative even if a host previously
// registered its own collect_result. No-op if the registry is not configured.
func (e *Engine) RegisterTextTools() {
	if e.Functions == nil {
		return
	}
	_ = e.Functions.RegisterOrReplace("collect_result", "استخراج از خروجی", e.collectResultFunction())
	_ = e.Functions.RegisterOrReplace("inspect_result", "بررسی خروجی", e.inspectResultFunction())
}

// CollectResultToolDefinition returns the OpenAI tool schema for collect_result.
func CollectResultToolDefinition() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: "collect_result",
			Description: "Extract specific information from ONE of YOUR OWN large/buffered tool results using a helper LLM. " +
				"When a tool result is too large it is buffered privately for you under a result_id (given in the truncation message). " +
				"Pass that result_id and a 'query' describing exactly what you need; a lightweight model returns just the relevant part. " +
				"Prefer `inspect_result` for cheap deterministic slicing (stats/head/tail/slice/grep); use collect_result when you need semantic extraction or summarization. " +
				"You can only access results you own — never another user's.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"result_id": map[string]interface{}{
						"type":        "string",
						"description": "The result_id from the truncation message (format: r_<sessionID>_<timestamp>). Must be one of your own buffered results.",
					},
					"query": map[string]interface{}{
						"type":        "string",
						"description": "What specific information to extract from the buffered result.",
					},
				},
				"required": []string{"result_id", "query"},
			},
		},
	}
}

// InspectResultToolDefinition returns the OpenAI tool schema for inspect_result.
func InspectResultToolDefinition() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: "inspect_result",
			Description: "Inspect ONE of YOUR OWN large/buffered tool results deterministically (no LLM, fast, free). " +
				"Operates only on results you own (result_id from the truncation message). Actions:\n" +
				"- stats: line count, character count, longest line, first-line preview — call this first to decide how to slice.\n" +
				"- head: first N lines (default 30) with line numbers.\n" +
				"- tail: last N lines (default 30) with line numbers.\n" +
				"- slice: lines from 'start' to 'end' (1-based, inclusive) with line numbers.\n" +
				"- grep: lines matching 'query' (regex; literal fallback) with line numbers; options: ignore_case, invert, context (surrounding lines), max_matches.\n" +
				"- unique: distinct lines, first-occurrence order (like `sort -u` without reordering).\n" +
				"- sort: lines sorted; options: desc (reverse), numeric (by a leading number).\n" +
				"- count: with 'query', number of matching lines (like `grep -c`, honors ignore_case/invert); without 'query', the frequency of each distinct line, most frequent first (like `sort | uniq -c`).",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"stats", "head", "tail", "slice", "grep", "unique", "sort", "count"},
						"description": "The inspection operation to perform.",
					},
					"result_id": map[string]interface{}{
						"type":        "string",
						"description": "The result_id from the truncation message. Must be one of your own buffered results.",
					},
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search pattern (regex, falls back to literal substring) for action=grep.",
					},
					"lines": map[string]interface{}{
						"type":        "integer",
						"description": "Number of lines for action=head/tail (default 30, max 500).",
					},
					"start": map[string]interface{}{
						"type":        "integer",
						"description": "First line (1-based, inclusive) for action=slice.",
					},
					"end": map[string]interface{}{
						"type":        "integer",
						"description": "Last line (1-based, inclusive) for action=slice.",
					},
					"context": map[string]interface{}{
						"type":        "integer",
						"description": "Lines of surrounding context to include around each match for action=grep (like grep -C, max 10).",
					},
					"ignore_case": map[string]interface{}{
						"type":        "boolean",
						"description": "Case-insensitive match for action=grep.",
					},
					"invert": map[string]interface{}{
						"type":        "boolean",
						"description": "Return non-matching lines for action=grep (like grep -v).",
					},
					"max_matches": map[string]interface{}{
						"type":        "integer",
						"description": "Stop after this many matches for action=grep (default/max 200).",
					},
					"desc": map[string]interface{}{
						"type":        "boolean",
						"description": "Reverse order (descending) for action=sort.",
					},
					"numeric": map[string]interface{}{
						"type":        "boolean",
						"description": "Sort by a leading number instead of lexically for action=sort.",
					},
				},
				"required": []string{"action", "result_id"},
			},
		},
	}
}

// collectResultFunction builds the ownership-enforcing collect_result implementation.
func (e *Engine) collectResultFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		userID, _ := args["__user_id__"].(string)
		sessionID, _ := args["__session_id__"].(string)

		resultID, err := getStringArg(args, "result_id")
		if err != nil {
			return "Error: result_id is required for collect_result.", nil
		}
		query := strings.TrimSpace(getOptStringArg(args, "query"))
		if query == "" {
			return "Error: query is required for collect_result.", nil
		}

		full, err := e.getOwnedToolResult(userID, sessionID, resultID)
		if err != nil {
			return "Error: " + err.Error(), nil
		}

		// The helper-LLM call is metered against the owner (trusted userID).
		ctx := context.Background()
		if userID != "" {
			ctx = model.WithUserID(ctx, userID)
		}
		out, err := e.extractFromResult(ctx, full, query)
		if err != nil {
			return "", err
		}
		return out, nil
	}
}

// inspectResultFunction builds the ownership-enforcing inspect_result implementation.
func (e *Engine) inspectResultFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		userID, _ := args["__user_id__"].(string)
		sessionID, _ := args["__session_id__"].(string)

		action, err := getStringArg(args, "action")
		if err != nil {
			return "Error: action is required for inspect_result (stats, head, tail, slice, or grep).", nil
		}
		resultID, err := getStringArg(args, "result_id")
		if err != nil {
			return "Error: result_id is required for inspect_result.", nil
		}

		full, err := e.getOwnedToolResult(userID, sessionID, resultID)
		if err != nil {
			return "Error: " + err.Error(), nil
		}

		switch strings.ToLower(strings.TrimSpace(action)) {
		case "stats":
			return inspectStats(full), nil
		case "head":
			return inspectHead(full, getIntArg(args, "lines", defaultInspectLines)), nil
		case "tail":
			return inspectTail(full, getIntArg(args, "lines", defaultInspectLines)), nil
		case "slice":
			start := getIntArg(args, "start", 1)
			end := getIntArg(args, "end", start+defaultInspectLines-1)
			return inspectSlice(full, start, end), nil
		case "grep":
			return inspectGrep(full, grepOpts{
				query:      getOptStringArg(args, "query"),
				ignoreCase: getBoolArg(args, "ignore_case"),
				invert:     getBoolArg(args, "invert"),
				context:    getIntArg(args, "context", 0),
				maxMatches: getIntArg(args, "max_matches", 0),
			}), nil
		case "unique":
			return inspectUnique(full), nil
		case "sort":
			return inspectSort(full, getBoolArg(args, "desc"), getBoolArg(args, "numeric")), nil
		case "count":
			return inspectCount(full, grepOpts{
				query:      getOptStringArg(args, "query"),
				ignoreCase: getBoolArg(args, "ignore_case"),
				invert:     getBoolArg(args, "invert"),
			}), nil
		default:
			return fmt.Sprintf("Error: unknown action %q. Use stats, head, tail, slice, grep, unique, sort, or count.", action), nil
		}
	}
}

// ----------------------------------------------------------------------------
// Deterministic inspection helpers (pure functions — unit tested directly).
// ----------------------------------------------------------------------------

func inspectStats(full string) string {
	lines := strings.Split(full, "\n")
	longest := 0
	for _, l := range lines {
		if len(l) > longest {
			longest = len(l)
		}
	}
	preview := ""
	if len(lines) > 0 {
		preview = strings.TrimRight(lines[0], "\r")
	}
	return fmt.Sprintf("lines=%d | chars=%d | longest_line=%d\nfirst line: %s",
		len(lines), len(full), longest, truncateForLog(preview, 200))
}

func inspectHead(full string, n int) string {
	if n <= 0 {
		n = defaultInspectLines
	}
	if n > maxInspectLines {
		n = maxInspectLines
	}
	lines := strings.Split(full, "\n")
	total := len(lines)
	if n > total {
		n = total
	}
	body := numberLines(lines[:n], 1)
	if n < total {
		body += fmt.Sprintf("\n... (%d more lines; use tail/slice/grep to see more)", total-n)
	}
	return capOutput(body)
}

func inspectTail(full string, n int) string {
	if n <= 0 {
		n = defaultInspectLines
	}
	if n > maxInspectLines {
		n = maxInspectLines
	}
	lines := strings.Split(full, "\n")
	total := len(lines)
	if n > total {
		n = total
	}
	startIdx := total - n
	body := numberLines(lines[startIdx:], startIdx+1)
	if startIdx > 0 {
		body = fmt.Sprintf("... (%d earlier lines omitted; use head/slice/grep)\n", startIdx) + body
	}
	return capOutput(body)
}

func inspectSlice(full string, start, end int) string {
	lines := strings.Split(full, "\n")
	total := len(lines)
	if start < 1 {
		start = 1
	}
	if start > total {
		return fmt.Sprintf("start line %d is past the end of the output (%d lines).", start, total)
	}
	if end < start {
		end = start
	}
	if end > total {
		end = total
	}
	if end-start+1 > maxInspectLines {
		end = start + maxInspectLines - 1
	}
	body := numberLines(lines[start-1:end], start)
	if end < total {
		body += fmt.Sprintf("\n... (%d more lines after %d)", total-end, end)
	}
	return capOutput(body)
}

type grepOpts struct {
	query      string
	ignoreCase bool
	invert     bool
	context    int
	maxMatches int
}

func inspectGrep(full string, o grepOpts) string {
	if strings.TrimSpace(o.query) == "" {
		return "Error: query is required for action=grep."
	}

	matcher := buildMatcher(o.query, o.ignoreCase)

	maxMatches := o.maxMatches
	if maxMatches <= 0 || maxMatches > maxInspectGrepMatches {
		maxMatches = maxInspectGrepMatches
	}
	ctxN := o.context
	if ctxN < 0 {
		ctxN = 0
	}
	if ctxN > maxGrepContext {
		ctxN = maxGrepContext
	}

	lines := strings.Split(full, "\n")
	var b strings.Builder
	matches := 0
	printed := make(map[int]bool)
	for i, line := range lines {
		hit := matcher(line)
		if o.invert {
			hit = !hit
		}
		if !hit {
			continue
		}
		matches++
		lo := i - ctxN
		if lo < 0 {
			lo = 0
		}
		hi := i + ctxN
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			if printed[j] {
				continue
			}
			printed[j] = true
			sep := ":" // matching line
			if j != i {
				sep = "-" // context line (grep -C convention)
			}
			b.WriteString(fmt.Sprintf("%d%s%s\n", j+1, sep, strings.TrimRight(lines[j], "\r")))
		}
		if matches >= maxMatches {
			b.WriteString(fmt.Sprintf("... (stopped at %d matches; narrow the pattern)\n", maxMatches))
			break
		}
	}
	if matches == 0 {
		return fmt.Sprintf("No matches for %q.", o.query)
	}
	return capOutput(strings.TrimRight(b.String(), "\n"))
}

// buildMatcher compiles query as a regex (honoring ignoreCase) and falls back
// to a literal substring match. Shared by grep and count.
func buildMatcher(query string, ignoreCase bool) func(string) bool {
	pattern := query
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	if re, err := regexp.Compile(pattern); err == nil {
		return re.MatchString
	}
	if ignoreCase {
		needle := strings.ToLower(query)
		return func(l string) bool { return strings.Contains(strings.ToLower(l), needle) }
	}
	return func(l string) bool { return strings.Contains(l, query) }
}

// inspectUnique returns the distinct lines, preserving first-occurrence order.
func inspectUnique(full string) string {
	lines := strings.Split(full, "\n")
	seen := make(map[string]struct{}, len(lines))
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	note := fmt.Sprintf("(%d distinct of %d lines)", len(out), len(lines))
	return renderLines(out, note)
}

// inspectSort returns the lines sorted lexicographically (or numerically by a
// leading number when numeric), ascending unless desc is set.
func inspectSort(full string, desc, numeric bool) string {
	lines := strings.Split(full, "\n")
	sorted := make([]string, len(lines))
	copy(sorted, lines)
	less := func(i, j int) bool { return sorted[i] < sorted[j] }
	if numeric {
		less = func(i, j int) bool {
			ni, nj := parseLeadingNumber(sorted[i]), parseLeadingNumber(sorted[j])
			if ni != nj {
				return ni < nj
			}
			return sorted[i] < sorted[j]
		}
	}
	sort.SliceStable(sorted, less)
	if desc {
		for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
			sorted[i], sorted[j] = sorted[j], sorted[i]
		}
	}
	dir := "asc"
	if desc {
		dir = "desc"
	}
	kind := "lexical"
	if numeric {
		kind = "numeric"
	}
	return renderLines(sorted, fmt.Sprintf("(%d lines, %s %s)", len(sorted), kind, dir))
}

// inspectCount has two modes. With a query it counts matching lines (like
// `grep -c`, honoring ignore_case/invert). Without a query it reports the
// frequency of each distinct line (like `sort | uniq -c`), most frequent first.
func inspectCount(full string, o grepOpts) string {
	lines := strings.Split(full, "\n")

	if strings.TrimSpace(o.query) != "" {
		matcher := buildMatcher(o.query, o.ignoreCase)
		n := 0
		for _, l := range lines {
			hit := matcher(l)
			if o.invert {
				hit = !hit
			}
			if hit {
				n++
			}
		}
		return fmt.Sprintf("%d of %d line(s) match %q.", n, len(lines), o.query)
	}

	freq := make(map[string]int, len(lines))
	order := make([]string, 0)
	for _, l := range lines {
		if _, ok := freq[l]; !ok {
			order = append(order, l)
		}
		freq[l]++
	}
	sort.SliceStable(order, func(i, j int) bool {
		if freq[order[i]] != freq[order[j]] {
			return freq[order[i]] > freq[order[j]]
		}
		return order[i] < order[j]
	})
	out := make([]string, 0, len(order))
	for _, l := range order {
		out = append(out, fmt.Sprintf("%6d  %s", freq[l], strings.TrimRight(l, "\r")))
	}
	return renderLines(out, fmt.Sprintf("(%d distinct of %d lines)", len(order), len(lines)))
}

// parseLeadingNumber reads a leading numeric prefix (int or float, optional
// sign) from a line, returning 0 when there is none.
func parseLeadingNumber(s string) float64 {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) {
		c := s[end]
		if (c >= '0' && c <= '9') || c == '.' || (end == 0 && (c == '-' || c == '+')) {
			end++
		} else {
			break
		}
	}
	if end == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0
	}
	return f
}

// renderLines joins lines (capped to maxInspectLines) and appends an optional
// summary note, then bounds the whole thing.
func renderLines(lines []string, note string) string {
	shown := lines
	if len(shown) > maxInspectLines {
		shown = shown[:maxInspectLines]
	}
	body := strings.Join(shown, "\n")
	if note != "" {
		if body != "" {
			body += "\n"
		}
		body += note
	}
	return capOutput(body)
}

// numberLines prefixes each line with its 1-based number starting at `start`.
func numberLines(lines []string, start int) string {
	var b strings.Builder
	for i, l := range lines {
		b.WriteString(fmt.Sprintf("%d: %s\n", start+i, strings.TrimRight(l, "\r")))
	}
	return strings.TrimRight(b.String(), "\n")
}

// capOutput bounds an inspection response, truncating on a rune boundary so a
// slice of a huge buffer can't itself overflow the context.
func capOutput(s string) string {
	if len(s) <= maxInspectResultChars {
		return s
	}
	cut := maxInspectResultChars
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n... (output truncated at %d bytes; narrow with grep/slice/head/tail)", cut)
}

// ----------------------------------------------------------------------------
// Small arg coercion helpers (JSON numbers arrive as float64).
// ----------------------------------------------------------------------------

func getOptStringArg(args map[string]interface{}, key string) string {
	s, _ := args[key].(string)
	return s
}

func getIntArg(args map[string]interface{}, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		if p, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return p
		}
	}
	return def
}

func getBoolArg(args map[string]interface{}, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(strings.TrimSpace(b), "true")
	}
	return false
}
