package engine

import (
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func msgs(n int) []openai.ChatCompletionMessage {
	out := make([]openai.ChatCompletionMessage, n)
	for i := 0; i < n; i++ {
		out[i] = openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: string(rune('a' + i))}
	}
	return out
}

func TestSplitRollingWindow(t *testing.T) {
	cases := []struct {
		name             string
		total, retain    int
		wantArch, wantKp int
	}{
		{"more than window", 30, 10, 20, 10},
		{"exactly window", 10, 10, 0, 10},
		{"fewer than window", 4, 10, 0, 4},
		{"empty", 0, 10, 0, 0},
		{"retain zero archives all", 5, 0, 5, 0},
		{"retain negative treated as zero", 5, -3, 5, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			arch, keep := splitRollingWindow(msgs(c.total), c.retain)
			if len(arch) != c.wantArch {
				t.Fatalf("archive: got %d want %d", len(arch), c.wantArch)
			}
			if len(keep) != c.wantKp {
				t.Fatalf("keep: got %d want %d", len(keep), c.wantKp)
			}
			// The kept messages must be the most recent ones (a contiguous tail).
			if len(keep) > 0 {
				all := msgs(c.total)
				if keep[len(keep)-1].Content != all[len(all)-1].Content {
					t.Fatalf("kept tail mismatch: last kept=%q want %q", keep[len(keep)-1].Content, all[len(all)-1].Content)
				}
			}
			if len(arch)+len(keep) != c.total {
				t.Fatalf("archive+keep=%d want total %d", len(arch)+len(keep), c.total)
			}
		})
	}
}

// toolConv builds a realistic conversation with two tool-call/result pairs:
//
//	[0] user
//	[1] assistant (ToolCalls=[X])
//	[2] tool     (ToolCallID=X)
//	[3] assistant (text response)
//	[4] user
//	[5] assistant (ToolCalls=[Y])
//	[6] tool     (ToolCallID=Y)
//	[7] assistant (text response)
func toolConv() []openai.ChatCompletionMessage {
	return []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "user1"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "X"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "X", Content: "resultX"},
		{Role: openai.ChatMessageRoleAssistant, Content: "resp1"},
		{Role: openai.ChatMessageRoleUser, Content: "user2"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "Y"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "Y", Content: "resultY"},
		{Role: openai.ChatMessageRoleAssistant, Content: "resp2"},
	}
}

func TestSplitRollingWindow_ToolCallPairNotSplit(t *testing.T) {
	// Verify that toKeep never starts with a "tool" message regardless of
	// where the naive cut lands.
	conv := toolConv() // 8 messages
	for retain := 0; retain <= len(conv)+2; retain++ {
		arch, keep := splitRollingWindow(conv, retain)
		if len(keep) > 0 && keep[0].Role == openai.ChatMessageRoleTool {
			t.Errorf("retain=%d: toKeep starts with role=tool (orphaned tool result) — arch=%d keep=%d",
				retain, len(arch), len(keep))
		}
		if len(arch)+len(keep) != len(conv) {
			t.Errorf("retain=%d: arch(%d)+keep(%d) != total(%d)", retain, len(arch), len(keep), len(conv))
		}
	}
}

func TestSplitRollingWindow_CutOnToolResult_ShiftsBack(t *testing.T) {
	// With retain=6 the naive cut is index 2 (the tool-result for X).
	// The fix must move it back to index 1 (the assistant with ToolCalls),
	// so toKeep=[1..7] and toArchive=[0].
	conv := toolConv() // 8 messages
	arch, keep := splitRollingWindow(conv, 6)
	if len(keep) == 0 || keep[0].Role == openai.ChatMessageRoleTool {
		t.Fatalf("expected keep to start before tool-result, got role=%q", keep[0].Role)
	}
	if len(arch)+len(keep) != len(conv) {
		t.Fatalf("arch(%d)+keep(%d) != 8", len(arch), len(keep))
	}
}
