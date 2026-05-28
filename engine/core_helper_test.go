package engine

import (
	"strings"
	"testing"
)

func TestIsNonsenseMessageFast(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"normal persian", "سلام خوبی؟", false},
		{"normal english", "hello world", false},
		{"persian sentence", "سلام من مسعود هستم", false},
		{"short question", "چرا؟", false},
		{"too short empty", "", true},
		{"too short 2 chars", "hi", true},
		{"persian 2 runes but 4 bytes", "سل", false},
		{"repeated chars", "aaaaaaaaaa", true},
		{"only special chars", "!@#$%^&*()", true},
		{"only digits long", "12345678901", true},
		{"long no space", strings.Repeat("x", 60), true},
		{"normal with numbers", "I have 3 cats", false},
		{"mixed valid", "test 123 hello", false},
		{"dominant char", "aaaaab", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNonsenseMessageFast(tt.input)
			if got != tt.want {
				t.Errorf("IsNonsenseMessageFast(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
