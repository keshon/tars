package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Stream sends req with stream:true and calls onDelta for each content
// chunk, returning the assembled ChatResponse. Only dialects with SSE
// support work: koboldcpp decides tool calls in its own server-side pass
// and has no stable stream contract, so it refuses and the caller falls
// back to ChatWithRetry.
func (c *Server) Stream(ctx context.Context, req ChatRequest, onDelta func(string)) (ChatResponse, error) {
	if _, ok := c.dialect.(koboldDialect); ok {
		return ChatResponse{}, fmt.Errorf("streaming unsupported on koboldcpp — use Chat")
	}
	temperature := req.Temperature
	if temperature <= 0 {
		temperature = defaultTemperature
	}
	wreq := wireRequest{
		Model:       c.model,
		MaxTokens:   req.MaxTokens,
		Temperature: temperature,
		TopP:        defaultTopP,
		TopK:        defaultTopK,
	}
	structured := req.Grammar != "" || req.JSONSchema != ""
	if structured {
		c.dialect.applyStructured(&wreq, req.Grammar, req.JSONSchema)
	}
	c.dialect.applySampling(&wreq, c.DRY)
	// Koboldcpp never reaches here (refused above), so images are
	// always encodable on the streaming path.
	msgs, err := encodeMessages(req.Messages, true)
	if err != nil {
		return ChatResponse{}, err
	}
	wreq.Messages = msgs

	for _, t := range req.Tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		wreq.Tools = append(wreq.Tools, wt)
	}

	body, err := json.Marshal(struct {
		wireRequest
		Stream bool `json:"stream"`
	}{wireRequest: wreq, Stream: true})
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.ExtraHeaders {
		if k != "" && v != "" {
			httpReq.Header.Set(k, v)
		}
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return ChatResponse{}, &APIError{Status: resp.StatusCode, StatusText: resp.Status, Body: string(raw)}
	}

	var content strings.Builder
	var reasoning strings.Builder
	var finish string
	complete := false
	type deltaCall struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	acc := map[int]*deltaCall{}
	var promptTokens, completionTokens int

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "[DONE]" {
			complete = true
			break
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta struct {
					Role      string      `json:"role"`
					Content   string      `json:"content"`
					Reasoning string      `json:"reasoning_content"`
					ToolCalls []deltaCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return ChatResponse{}, fmt.Errorf("invalid stream frame: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return ChatResponse{}, fmt.Errorf("stream backend error: %s", chunk.Error)
		}
		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
			completionTokens = chunk.Usage.CompletionTokens
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				finish = ch.FinishReason
				complete = true
			}
			if ch.Delta.Content != "" {
				content.WriteString(ch.Delta.Content)
				if onDelta != nil {
					onDelta(ch.Delta.Content)
				}
			}
			// Reasoning deltas accumulate silently: deliberation is
			// measured by the budget, not displayed live.
			if ch.Delta.Reasoning != "" {
				reasoning.WriteString(ch.Delta.Reasoning)
			}
			for _, dc := range ch.Delta.ToolCalls {
				a := acc[dc.Index]
				if a == nil {
					cp := dc
					a = &cp
					acc[dc.Index] = a
				} else {
					a.Function.Arguments += dc.Function.Arguments
					if dc.ID != "" {
						a.ID = dc.ID
					}
					if dc.Function.Name != "" {
						a.Function.Name = dc.Function.Name
					}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read stream: %w", err)
	}
	if !complete {
		return ChatResponse{}, fmt.Errorf("incomplete stream: EOF before completion")
	}

	msg := Message{Role: RoleAssistant, Content: content.String(), Reasoning: reasoning.String()}
	for i := 0; i < len(acc); i++ {
		dc, ok := acc[i]
		if !ok {
			continue
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:        dc.ID,
			Name:      dc.Function.Name,
			Arguments: repairArguments(normalizeArguments(json.RawMessage(dc.Function.Arguments))),
		})
	}
	// llama.cpp streams carry no usage block: estimate from the wire
	// instead of reporting a dead meter (marked Estimated; displays
	// show "~"). Same chars/4 ratio the reasoning budget uses.
	estimated := false
	if promptTokens == 0 {
		estimated = true
		n := 0
		for _, m := range req.Messages {
			n += len([]rune(m.Content)) + len([]rune(m.Reasoning))
		}
		if toolsJSON, err := json.Marshal(req.Tools); err == nil {
			n += len([]rune(string(toolsJSON)))
		}
		promptTokens = n / 4
	}
	if completionTokens == 0 {
		estimated = true
		completionTokens = (len([]rune(content.String())) + len([]rune(reasoning.String()))) / 4
	}
	c.recordUsage(promptTokens, completionTokens, 0)
	p, g, n := c.UsageTotals()
	c.logDebug("[%s] --- usage (stream: %d prompt + %d generated; run totals: %d prompt + %d generated over %d calls) ---\n\n",
		time.Now().Format(time.RFC3339), promptTokens, completionTokens, p, g, n)
	return ChatResponse{Message: msg, FinishReason: finish, Usage: Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Estimated:        estimated,
	}}, nil
}
