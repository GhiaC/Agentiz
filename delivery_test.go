package agentize

import (
	"context"
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

type fakeUserMessageProcessor struct {
	response string
	files    []*model.UserFile
	err      error
}

func (f *fakeUserMessageProcessor) ProcessMessageWithGeneratedFiles(
	context.Context,
	string,
	string,
) (string, []*model.UserFile, error) {
	return f.response, f.files, f.err
}

func TestProcessUserMessageAndDeliverSendsGeneratedFileBytes(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	ag, err := NewWithOptions(createTestKnowledgeTree(t), &Options{
		SessionStore: sqliteStore,
		FileStoreDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := ag.CreateSession("user-1")
	if err != nil {
		t.Fatal(err)
	}
	file, err := ag.RecordUserFile(
		session.SessionID,
		"browser.png",
		"image/png",
		model.FileSourceGenerated,
		[]byte("PNG-DATA"),
	)
	if err != nil {
		t.Fatal(err)
	}

	var sentUser string
	var sentFile *model.UserFile
	var sentData []byte
	sender := GeneratedFileSenderFunc(func(
		_ context.Context,
		userID string,
		file *model.UserFile,
		data []byte,
	) error {
		sentUser = userID
		sentFile = file
		sentData = append([]byte(nil), data...)
		return nil
	})

	response, err := ag.ProcessUserMessageAndDeliver(
		context.Background(),
		&fakeUserMessageProcessor{response: "done", files: []*model.UserFile{file}},
		"user-1",
		"take a screenshot",
		sender,
	)
	if err != nil {
		t.Fatal(err)
	}
	if response != "done" || sentUser != "user-1" || sentFile.FileID != file.FileID || string(sentData) != "PNG-DATA" {
		t.Fatalf(
			"unexpected delivery: response=%q user=%q file=%+v data=%q",
			response,
			sentUser,
			sentFile,
			sentData,
		)
	}
}

func TestDeliverGeneratedFilesEnforcesOwner(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	ag, err := NewWithOptions(createTestKnowledgeTree(t), &Options{
		SessionStore: sqliteStore,
		FileStoreDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := ag.CreateSession("owner")
	if err != nil {
		t.Fatal(err)
	}
	file, err := ag.RecordUserFile(
		session.SessionID,
		"secret.txt",
		"text/plain",
		model.FileSourceGenerated,
		[]byte("secret"),
	)
	if err != nil {
		t.Fatal(err)
	}

	called := false
	err = ag.DeliverGeneratedFiles(
		context.Background(),
		"intruder",
		[]*model.UserFile{file},
		GeneratedFileSenderFunc(func(context.Context, string, *model.UserFile, []byte) error {
			called = true
			return nil
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("expected owner refusal, got %v", err)
	}
	if called {
		t.Fatal("sender must not receive another user's file")
	}
}
