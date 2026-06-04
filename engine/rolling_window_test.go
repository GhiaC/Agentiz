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
