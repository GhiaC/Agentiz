package engine

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func TestInspectHeadTailSlice(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "line" + strconv.Itoa(i+1)
	}
	full := strings.Join(lines, "\n")

	head := inspectHead(full, 30)
	if !strings.HasPrefix(head, "1: line1\n") {
		t.Fatalf("head should start at line 1, got:\n%s", head)
	}
	if !strings.Contains(head, "30: line30") {
		t.Fatalf("head should include line 30, got:\n%s", head)
	}
	if strings.Contains(head, "31: line31") {
		t.Fatalf("head should not include line 31, got:\n%s", head)
	}
	if !strings.Contains(head, "70 more lines") {
		t.Fatalf("head should note remaining lines, got:\n%s", head)
	}

	tail := inspectTail(full, 10)
	if !strings.Contains(tail, "100: line100") {
		t.Fatalf("tail should include last line, got:\n%s", tail)
	}
	if !strings.Contains(tail, "91: line91") || strings.Contains(tail, "90: line90") {
		t.Fatalf("tail should be the last 10 lines, got:\n%s", tail)
	}

	slice := inspectSlice(full, 45, 47)
	if !strings.Contains(slice, "45: line45") || !strings.Contains(slice, "47: line47") {
		t.Fatalf("slice 45-47 missing bounds, got:\n%s", slice)
	}
	if strings.Contains(slice, "44: line44") || strings.Contains(slice, "48: line48") {
		t.Fatalf("slice 45-47 leaked neighbors, got:\n%s", slice)
	}
}

func TestInspectSliceOutOfRange(t *testing.T) {
	full := "a\nb\nc"
	if got := inspectSlice(full, 10, 20); !strings.Contains(got, "past the end") {
		t.Fatalf("expected past-end message, got %q", got)
	}
}

func TestInspectGrep(t *testing.T) {
	full := "apple\nbanana\nAPRICOT\ncherry\napex"

	// Literal/regex, case sensitive.
	got := inspectGrep(full, grepOpts{query: "^ap"})
	if !strings.Contains(got, "1:apple") || !strings.Contains(got, "5:apex") {
		t.Fatalf("regex ^ap should match apple+apex, got:\n%s", got)
	}
	if strings.Contains(got, "APRICOT") {
		t.Fatalf("case-sensitive should not match APRICOT, got:\n%s", got)
	}

	// Case-insensitive.
	ci := inspectGrep(full, grepOpts{query: "^ap", ignoreCase: true})
	if !strings.Contains(ci, "APRICOT") {
		t.Fatalf("ignore_case should match APRICOT, got:\n%s", ci)
	}

	// Context lines use '-' separator.
	ctx := inspectGrep(full, grepOpts{query: "banana", context: 1})
	if !strings.Contains(ctx, "1-apple") || !strings.Contains(ctx, "2:banana") || !strings.Contains(ctx, "3-APRICOT") {
		t.Fatalf("context grep wrong, got:\n%s", ctx)
	}

	// Invert.
	inv := inspectGrep(full, grepOpts{query: "a", invert: true})
	if !strings.Contains(inv, "cherry") || strings.Contains(inv, "banana") {
		t.Fatalf("invert grep wrong, got:\n%s", inv)
	}

	// No match.
	if got := inspectGrep(full, grepOpts{query: "zzz"}); !strings.Contains(got, "No matches") {
		t.Fatalf("expected no-match message, got %q", got)
	}
}

func TestInspectUnique(t *testing.T) {
	full := "b\na\nb\nc\na\nb"
	got := inspectUnique(full)
	// First-occurrence order preserved: b, a, c.
	if !strings.HasPrefix(got, "b\na\nc\n") {
		t.Fatalf("unique should keep first-occurrence order, got:\n%s", got)
	}
	if !strings.Contains(got, "3 distinct of 6 lines") {
		t.Fatalf("unique should note counts, got:\n%s", got)
	}
}

func TestInspectSort(t *testing.T) {
	full := "banana\napple\ncherry"
	asc := inspectSort(full, false, false)
	if !strings.HasPrefix(asc, "apple\nbanana\ncherry") {
		t.Fatalf("asc sort wrong, got:\n%s", asc)
	}
	desc := inspectSort(full, true, false)
	if !strings.HasPrefix(desc, "cherry\nbanana\napple") {
		t.Fatalf("desc sort wrong, got:\n%s", desc)
	}

	// Numeric: lexical would put "10" before "9"; numeric must not.
	num := inspectSort("10\n9\n100\n2", false, true)
	if !strings.HasPrefix(num, "2\n9\n10\n100") {
		t.Fatalf("numeric sort wrong, got:\n%s", num)
	}
}

func TestInspectCount(t *testing.T) {
	full := "err\nok\nerr\nok\nerr"

	// Frequency mode (no query), most frequent first.
	freq := inspectCount(full, grepOpts{})
	if !strings.Contains(freq, "3  err") || !strings.Contains(freq, "2  ok") {
		t.Fatalf("frequency count wrong, got:\n%s", freq)
	}
	if strings.Index(freq, "err") > strings.Index(freq, "ok") {
		t.Fatalf("err (3) should come before ok (2), got:\n%s", freq)
	}

	// Query mode: count matching lines.
	m := inspectCount(full, grepOpts{query: "err"})
	if !strings.Contains(m, "3 of 5 line(s) match") {
		t.Fatalf("match count wrong, got:\n%s", m)
	}

	// Query mode, inverted.
	inv := inspectCount(full, grepOpts{query: "err", invert: true})
	if !strings.Contains(inv, "2 of 5 line(s) match") {
		t.Fatalf("inverted match count wrong, got:\n%s", inv)
	}
}

func TestCapOutputRuneSafe(t *testing.T) {
	big := strings.Repeat("سلام", 5000) // multibyte, well over the cap
	out := capOutput(big)
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation notice")
	}
	// The truncated prefix (before the notice) must be valid UTF-8.
	prefix := out[:strings.Index(out, "\n... (output truncated")]
	if !utf8.ValidString(prefix) {
		t.Fatalf("capOutput cut a UTF-8 rune")
	}
}

// TestGetOwnedToolResult_CrossUserDenied is the core security test: a buffered
// result belongs to one user's session and MUST NOT be retrievable via another
// user's session/user identity, even when the correct result_id is presented.
func TestGetOwnedToolResult_CrossUserDenied(t *testing.T) {
	st, err := store.NewDBStoreWithPath(":memory:")
	if err != nil {
		t.Fatalf("NewDBStoreWithPath: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := &Engine{Sessions: st, llmConfig: LLMConfig{MaxToolResultLength: 50}}

	// User u1 buffers an oversized result on their own session.
	const u1Session = "u1-low-s0001"
	if err := st.Put(model.NewSessionWithID("u1", u1Session, model.AgentTypeLow)); err != nil {
		t.Fatalf("seed u1 session: %v", err)
	}
	loaded, _ := eng.Sessions.Get(u1Session)
	secret := strings.Repeat("SECRET-", 20)
	eng.processToolResult(loaded, secret)
	if err := eng.Sessions.Put(loaded); err != nil {
		t.Fatalf("persist u1: %v", err)
	}
	var resultID string
	for k := range loaded.ToolResults {
		resultID = k
	}
	if resultID == "" {
		t.Fatal("no result buffered")
	}

	// The rightful owner can read it.
	if got, err := eng.getOwnedToolResult("u1", u1Session, resultID); err != nil || got != secret {
		t.Fatalf("owner should read own result: got=%q err=%v", got, err)
	}

	// Attacker u2 with their own valid session tries the same result_id.
	const u2Session = "u2-low-s0001"
	if err := st.Put(model.NewSessionWithID("u2", u2Session, model.AgentTypeLow)); err != nil {
		t.Fatalf("seed u2 session: %v", err)
	}
	// Presenting u2's own session with u1's result_id: session mismatch → denied.
	if _, err := eng.getOwnedToolResult("u2", u2Session, resultID); err == nil {
		t.Fatal("SECURITY: u2 must not read u1's buffered result via own session")
	}
	// Presenting u1's session id but attacker's user id: user mismatch → denied.
	if _, err := eng.getOwnedToolResult("u2", u1Session, resultID); err == nil {
		t.Fatal("SECURITY: user-id mismatch must be denied even with correct session id")
	}
	// Empty caller session (skips the session-equality guard) but wrong user id
	// must still be denied by the user-ownership check.
	if _, err := eng.getOwnedToolResult("u2", "", resultID); err == nil {
		t.Fatal("SECURITY: user-id mismatch must be denied when session is unset")
	}
}
