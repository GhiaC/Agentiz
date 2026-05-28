package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	llminterface "github.com/ghiac/agentize/llm-interface"
	"github.com/sashabaranov/go-openai"
)

type mockProvider struct {
	resp  *llminterface.Response
	err   error
	calls int
}

func (m *mockProvider) ChatCompletion(_ context.Context, _ string, _ []llminterface.Message, _ []llminterface.Tool) (*llminterface.Response, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func TestBackupChain_TryBackup_FirstSuccess(t *testing.T) {
	p := &mockProvider{resp: &llminterface.Response{Content: "hello", Usage: llminterface.Usage{TotalTokens: 10}}}
	bc := NewBackupChain([]BackupLLM{{Provider: p, Model: "m1", Name: "test"}})

	msgs := []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}
	resp, ok := bc.TryBackup(context.Background(), msgs, nil, "test")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "hello" {
		t.Errorf("unexpected response content")
	}
	if p.calls != 1 {
		t.Errorf("expected 1 call, got %d", p.calls)
	}
}

func TestBackupChain_TryBackup_SkipsCooldown(t *testing.T) {
	p1 := &mockProvider{err: fmt.Errorf("fail")}
	p2 := &mockProvider{resp: &llminterface.Response{Content: "from-p2"}}
	bc := NewBackupChain([]BackupLLM{
		{Provider: p1, Model: "m1", Name: "provider1"},
		{Provider: p2, Model: "m2", Name: "provider2"},
	})

	msgs := []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}

	// First call: p1 fails, p2 succeeds
	resp, ok := bc.TryBackup(context.Background(), msgs, nil, "test")
	if !ok {
		t.Fatal("expected ok=true from p2")
	}
	if resp.Choices[0].Message.Content != "from-p2" {
		t.Errorf("expected from-p2, got %s", resp.Choices[0].Message.Content)
	}

	// Second call: p1 should be in cooldown, p2 called directly
	p1.calls = 0
	p2.calls = 0
	resp, ok = bc.TryBackup(context.Background(), msgs, nil, "test")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if p1.calls != 0 {
		t.Errorf("expected p1 skipped (cooldown), but got %d calls", p1.calls)
	}
	if p2.calls != 1 {
		t.Errorf("expected p2 called once, got %d", p2.calls)
	}
}

func TestBackupChain_TryBackup_EmptyResponse(t *testing.T) {
	p := &mockProvider{resp: &llminterface.Response{Content: "", Usage: llminterface.Usage{}}}
	bc := NewBackupChain([]BackupLLM{{Provider: p, Model: "m1", Name: "test"}})

	msgs := []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}
	_, ok := bc.TryBackup(context.Background(), msgs, nil, "test")
	if ok {
		t.Fatal("expected ok=false for empty response")
	}
}

func TestBackupChain_TryBackup_AllFail(t *testing.T) {
	p1 := &mockProvider{err: fmt.Errorf("fail1")}
	p2 := &mockProvider{err: fmt.Errorf("fail2")}
	bc := NewBackupChain([]BackupLLM{
		{Provider: p1, Model: "m1", Name: "a"},
		{Provider: p2, Model: "m2", Name: "b"},
	})

	msgs := []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}
	_, ok := bc.TryBackup(context.Background(), msgs, nil, "test")
	if ok {
		t.Fatal("expected ok=false when all fail")
	}
}

func TestBackupChain_Nil(t *testing.T) {
	bc := NewBackupChain(nil)
	if bc != nil {
		t.Fatal("expected nil chain for empty providers")
	}
	var nilChain *BackupChain
	_, ok := nilChain.TryBackup(context.Background(), nil, nil, "test")
	if ok {
		t.Fatal("expected ok=false from nil chain")
	}
}

// Verify cooldown duration is as expected
func TestBackupChain_CooldownDuration(t *testing.T) {
	if backupCooldownDuration != 1*time.Second {
		t.Errorf("expected 1s cooldown, got %s", backupCooldownDuration)
	}
}
