package agentize

import (
	"context"
	"errors"
	"fmt"

	"github.com/ghiac/agentize/model"
)

// GeneratedFileSender is implemented by a chat transport (Telegram, web chat,
// Discord, etc.) to turn a generated UserFile into a real outbound attachment.
// The sender may choose sendPhoto/sendImage for image MIME types and a generic
// document/file API for everything else.
type GeneratedFileSender interface {
	SendGeneratedFile(
		ctx context.Context,
		userID string,
		file *model.UserFile,
		data []byte,
	) error
}

// GeneratedFileSenderFunc adapts a function to GeneratedFileSender.
type GeneratedFileSenderFunc func(
	ctx context.Context,
	userID string,
	file *model.UserFile,
	data []byte,
) error

// SendGeneratedFile implements GeneratedFileSender.
func (f GeneratedFileSenderFunc) SendGeneratedFile(
	ctx context.Context,
	userID string,
	file *model.UserFile,
	data []byte,
) error {
	return f(ctx, userID, file, data)
}

// DeliverGeneratedFiles owner-checks, loads, and sends every generated file.
// It attempts all files and returns errors.Join of any failures so transports can
// log/retry partial delivery without losing the text response.
func (ag *Agentize) DeliverGeneratedFiles(
	ctx context.Context,
	userID string,
	files []*model.UserFile,
	sender GeneratedFileSender,
) error {
	if sender == nil {
		return fmt.Errorf("generated file sender is not configured")
	}

	var deliveryErrors []error
	for _, candidate := range files {
		if candidate == nil {
			continue
		}
		data, file, err := ag.ReadUserFileForUser(userID, candidate.FileID)
		if err != nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("load generated file %s: %w", candidate.FileID, err))
			continue
		}
		if err := sender.SendGeneratedFile(ctx, userID, file, data); err != nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("send generated file %s: %w", candidate.FileID, err))
		}
	}
	return errors.Join(deliveryErrors...)
}

// ProcessMessageAndDeliver is the adapter-ready entry point for direct Agentize
// sessions. It sends generated attachments before returning. The text and token
// count remain available even when attachment delivery returns an error.
func (ag *Agentize) ProcessMessageAndDeliver(
	ctx context.Context,
	sessionID string,
	userMessage string,
	sender GeneratedFileSender,
) (string, int, error) {
	response, tokens, files, processErr := ag.ProcessMessageWithGeneratedFiles(ctx, sessionID, userMessage)
	if len(files) == 0 {
		return response, tokens, processErr
	}

	session, sessionErr := ag.GetSessionStore().Get(sessionID)
	if sessionErr != nil {
		return response, tokens, errors.Join(processErr, fmt.Errorf("load session for generated file delivery: %w", sessionErr))
	}
	deliveryErr := ag.DeliverGeneratedFiles(ctx, session.UserID, files, sender)
	return response, tokens, errors.Join(processErr, deliveryErr)
}

// UserMessageWithGeneratedFilesProcessor is satisfied by
// core.CoreHandler.ProcessMessageWithGeneratedFiles. It intentionally uses an
// interface so Agentize does not need to depend on a particular bot or Core.
type UserMessageWithGeneratedFilesProcessor interface {
	ProcessMessageWithGeneratedFiles(
		ctx context.Context,
		userID string,
		userMessage string,
	) (string, []*model.UserFile, error)
}

// ProcessUserMessageAndDeliver is the adapter-ready entry point for Core-based
// chatbots. Core tracks files across all worker sessions for the user; Agentize
// resolves the bytes from the shared file store and invokes the transport.
func (ag *Agentize) ProcessUserMessageAndDeliver(
	ctx context.Context,
	processor UserMessageWithGeneratedFilesProcessor,
	userID string,
	userMessage string,
	sender GeneratedFileSender,
) (string, error) {
	if processor == nil {
		return "", fmt.Errorf("message processor is not configured")
	}
	response, files, processErr := processor.ProcessMessageWithGeneratedFiles(ctx, userID, userMessage)
	if len(files) == 0 {
		return response, processErr
	}
	deliveryErr := ag.DeliverGeneratedFiles(ctx, userID, files, sender)
	return response, errors.Join(processErr, deliveryErr)
}
