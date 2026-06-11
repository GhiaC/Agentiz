package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// UseVisionLLMConfig configures a separate LLM client for image processing.
func (ch *CoreHandler) UseVisionLLMConfig(config engine.LLMConfig) error {
	openaiConfig := openai.DefaultConfig(config.APIKey)
	if config.BaseURL != "" {
		openaiConfig.BaseURL = config.BaseURL
	}
	if config.HTTPClient != nil {
		openaiConfig.HTTPClient = config.HTTPClient
	}

	ch.visionLLMClient = openai.NewClientWithConfig(openaiConfig)
	ch.visionLLMConfig = &config

	log.Log.Infof("[CoreHandler] ✅ Vision LLM configured | Model: %s | BaseURL: %s", config.Model, config.BaseURL)
	return nil
}

// ProcessMessageWithImage handles messages that include an image.
func (ch *CoreHandler) ProcessMessageWithImage(
	ctx context.Context,
	userID string,
	userMessage string,
	imageData []byte,
	imageMimeType string,
) (string, error) {
	if ch.userProgress.TryQueue(userID, userMessage) {
		return "⏳ Processing previous request... Please wait. 📋 Your message was queued and will be answered in order.", nil
	}
	userMu := ch.getUserMutex(userID)
	userMu.Lock()
	defer userMu.Unlock()
	ch.userProgress.SetInProgress(userID, true)
	defer ch.userProgress.SetInProgress(userID, false)

	log.Log.Infof("[CoreHandler] 🖼️  Processing image message | UserID: %s | Message length: %d chars | Image size: %d bytes | MimeType: %s",
		userID, len(userMessage), len(imageData), imageMimeType)

	if !ch.agents.IsReady() {
		return "", fmt.Errorf("database is not ready. Call Init() on agent engines first")
	}

	llmClient := ch.visionLLMClient
	llmModel := ""
	if ch.visionLLMConfig != nil {
		llmModel = ch.visionLLMConfig.Model
	}

	if llmClient == nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Vision LLM not configured, falling back to main LLM")
		llmClient = ch.llmClient
		llmModel = ch.llmConfig.Model
	}

	if llmClient == nil {
		return "", fmt.Errorf("LLM client not configured. Call UseLLMConfig first")
	}

	if llmModel == "" {
		llmModel = "openai/gpt-5-nano"
	}

	if ch.userModeration != nil {
		if isBanned, banMessage := ch.userModeration.CheckBanStatus(userID); isBanned {
			return banMessage, nil
		}
	}

	coreSession, err := ch.getOrCreateCoreSession(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get or create core session: %w", err)
	}

	// Record the inbound image as a user file (best-effort) when a recorder is wired.
	if ch.fileRecorder != nil {
		imageName := "image" + imageExtension(imageMimeType)
		recStart := time.Now()
		_, recErr := ch.fileRecorder(coreSession.SessionID, imageName, imageMimeType, model.FileSourceUploaded, imageData)
		metrics.FileOp("upload", metrics.Status(recErr), time.Since(recStart))
		if recErr != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to record uploaded image as user file: %v", recErr)
		}
	}

	base64Image := base64.StdEncoding.EncodeToString(imageData)
	dataURL := fmt.Sprintf("data:%s;base64,%s", imageMimeType, base64Image)

	userMsg := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		MultiContent: []openai.ChatMessagePart{
			{
				Type: openai.ChatMessagePartTypeText,
				Text: userMessage,
			},
			{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL:    dataURL,
					Detail: openai.ImageURLDetailAuto,
				},
			},
		},
	}

	historyContent := userMessage
	if historyContent == "" {
		historyContent = "(User sent an image)"
	} else {
		historyContent = fmt.Sprintf("(User sent an image) %s", userMessage)
	}
	coreSession.Msgs = append(
		coreSession.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: historyContent,
		},
	)

	coreSession.Model = llmModel

	imageMsgID, imageSeqID := coreSession.GenerateMessageIDWithSeq()
	userMsgRecord := model.NewUserMessage(imageMsgID, imageSeqID, userID, coreSession.SessionID, historyContent, model.ContentTypeImage)
	ch.saveMessage(userMsgRecord)

	systemPrompts, err := ch.buildSystemPrompts(userID)
	if err != nil {
		return "", fmt.Errorf("failed to build system prompts: %w", err)
	}

	messages := []openai.ChatCompletionMessage{}
	for _, prompt := range systemPrompts {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: prompt,
		})
	}

	historyMsgs := coreSession.Msgs
	if len(historyMsgs) > 1 {
		messages = append(messages, historyMsgs[:len(historyMsgs)-1]...)
	}

	messages = append(messages, userMsg)

	ctx = model.WithUserID(ctx, userID)

	log.Log.Infof("[CoreHandler] 🔵 VISION LLM >> Model: %s | Messages: %d | Image included", llmModel, len(messages))

	request := openai.ChatCompletionRequest{
		Model:    llmModel,
		Messages: messages,
	}

	// Billing: meter (and allow blocking of) the vision LLM call. Image input was
	// previously instrumented for Prometheus but never sent to the usage Callback.
	visionMeta := map[string]interface{}{"media": "image", "kind": "vision"}
	if ch.Callback != nil {
		if cbErr := ch.Callback.BeforeAction(ctx, &engine.UsageEvent{
			UserID:    userID,
			SessionID: coreSession.SessionID,
			EventType: engine.EventLLMCall,
			Name:      engine.EventNameLLMCall,
			Model:     llmModel,
			Metadata:  visionMeta,
		}); cbErr != nil {
			return cbErr.Error(), nil
		}
	}

	visionStart := time.Now()
	resp, err := llmClient.CreateChatCompletion(ctx, request)
	visionDur := time.Since(visionStart)
	if err != nil {
		metrics.LLMCall("vision", llmModel, "error", visionDur, 0, 0, 0)
		if ch.Callback != nil {
			ch.Callback.AfterAction(ctx, &engine.UsageEvent{
				UserID: userID, SessionID: coreSession.SessionID,
				EventType: engine.EventLLMCall, Name: engine.EventNameLLMCall,
				Model: llmModel, Duration: visionDur, Error: err, Metadata: visionMeta,
			})
		}
		return "", fmt.Errorf("vision LLM call failed: %w", err)
	}
	visionCached := 0
	if resp.Usage.PromptTokensDetails != nil {
		visionCached = resp.Usage.PromptTokensDetails.CachedTokens
	}
	metrics.LLMCall("vision", llmModel, "ok", visionDur, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, visionCached)
	if ch.Callback != nil {
		ch.Callback.AfterAction(ctx, &engine.UsageEvent{
			UserID: userID, SessionID: coreSession.SessionID,
			EventType: engine.EventLLMCall, Name: engine.EventNameLLMCall,
			Model: llmModel, Tokens: resp.Usage.TotalTokens,
			InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens,
			CachedInputTokens: visionCached, Duration: visionDur, Metadata: visionMeta,
		})
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from vision LLM")
	}

	response := resp.Choices[0].Message.Content

	if resp.Usage.TotalTokens > 0 {
		log.Log.Infof("[CoreHandler] 📊 VISION TOKEN USAGE >> Model: %s | prompt=%d | completion=%d | total=%d",
			llmModel, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
	}

	coreSession.Msgs = append(
		coreSession.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: response,
		},
	)
	coreSession.UpdatedAt = time.Now()

	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}

	assistantMsgID, assistantSeqID := coreSession.GenerateMessageIDWithSeq()
	assistantMsg := model.NewMessage(
		assistantMsgID,
		assistantSeqID,
		userID,
		coreSession.SessionID,
		openai.ChatMessageRoleAssistant,
		response,
		model.AgentTypeCore,
		model.ContentTypeImage,
		request,
		resp,
		resp.Choices[0],
	)
	ch.saveMessage(assistantMsg)

	log.Log.Infof("[CoreHandler] ✅ Image message processed | UserID: %s | Response length: %d chars | Model: %s", userID, len(response), llmModel)

	return response, nil
}

// HasVisionLLM returns true if a Vision LLM is configured.
func (ch *CoreHandler) HasVisionLLM() bool {
	return ch.visionLLMClient != nil && ch.visionLLMConfig != nil
}

// imageExtension returns a file extension for a known image MIME type.
func imageExtension(mimeType string) string {
	switch mimeType {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".img"
	}
}
