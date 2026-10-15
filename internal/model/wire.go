package model

import (
	"encoding/json"
	"strings"
)

type outputItem struct {
	Type      string `json:"type"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

func visibleMessage(m Message) (Message, error) {
	if len(m.Items) == 0 {
		return m, nil
	}
	if m.Role != "assistant" {
		return Message{}, failure("invalid_request", "output items require assistant role")
	}
	parsed, err := parseItems(m.Items)
	if err != nil {
		return Message{}, failure("invalid_request", "invalid stored output items")
	}
	m.Content, m.Calls = parsed.Text, parsed.Calls
	return m, nil
}

func responsesRequest(req Request) (map[string]any, error) {
	input := []any{}
	for _, original := range req.Messages {
		if len(original.Items) > 0 && original.SourceModel == req.Model {
			for _, item := range original.Items {
				input = append(input, item)
			}
			continue
		}
		m, err := visibleMessage(original)
		if err != nil {
			return nil, err
		}
		if m.Role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.CallID, "output": m.Content})
			continue
		}
		if m.Content != "" || len(m.Images) > 0 {
			if len(m.Images) == 0 {
				input = append(input, map[string]any{"role": m.Role, "content": m.Content})
			} else {
				parts := []any{}
				if m.Content != "" {
					parts = append(parts, map[string]string{"type": "input_text", "text": m.Content})
				}
				for _, image := range m.Images {
					parts = append(parts, map[string]string{"type": "input_image", "image_url": image})
				}
				input = append(input, map[string]any{"role": m.Role, "content": parts})
			}
		}
		for _, call := range m.Calls {
			input = append(input, map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
		}
	}
	payload := map[string]any{"model": req.Model, "input": input, "store": false, "stream": false, "reasoning": map[string]string{"effort": req.Effort}, "include": []string{"reasoning.encrypted_content"}}
	if req.System != "" {
		payload["instructions"] = req.System
	}
	if len(req.Tools) > 0 {
		tools := []any{}
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters, "strict": false})
		}
		payload["tools"] = tools
	}
	if req.ToolChoice != "" {
		if req.ToolChoice == "auto" || req.ToolChoice == "none" || req.ToolChoice == "required" {
			payload["tool_choice"] = req.ToolChoice
		} else {
			payload["tool_choice"] = map[string]string{"type": "function", "name": req.ToolChoice}
		}
	}
	return payload, nil
}

func chatRequest(req Request) (map[string]any, error) {
	messages := []any{}
	if req.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.System})
	}
	for _, original := range req.Messages {
		m, err := visibleMessage(original)
		if err != nil {
			return nil, err
		}
		wire := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == "tool" {
			wire["tool_call_id"] = m.CallID
		}
		if len(m.Calls) > 0 {
			calls := []any{}
			for _, call := range m.Calls {
				calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments}})
			}
			wire["tool_calls"] = calls
		}
		if len(m.Images) > 0 {
			parts := []any{}
			if m.Content != "" {
				parts = append(parts, map[string]string{"type": "text", "text": m.Content})
			}
			for _, image := range m.Images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}})
			}
			wire["content"] = parts
		}
		messages = append(messages, wire)
	}
	payload := map[string]any{"model": req.Model, "messages": messages, "stream": false, "store": false, "reasoning_effort": req.Effort}
	if len(req.Tools) > 0 {
		tools := []any{}
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters, "strict": false}})
		}
		payload["tools"] = tools
	}
	if req.ToolChoice != "" {
		if req.ToolChoice == "auto" || req.ToolChoice == "none" || req.ToolChoice == "required" {
			payload["tool_choice"] = req.ToolChoice
		} else {
			payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": req.ToolChoice}}
		}
	}
	return payload, nil
}

func parseResponses(body []byte) (Response, error) {
	var wire struct {
		Status string            `json:"status"`
		Error  json.RawMessage   `json:"error"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			Input   int `json:"input_tokens"`
			Output  int `json:"output_tokens"`
			Details struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &wire) != nil {
		return Response{}, failure("invalid_response", "invalid JSON")
	}
	usage := Usage{InputTokens: wire.Usage.Input, OutputTokens: wire.Usage.Output, ReasoningTokens: wire.Usage.Details.Reasoning, TotalTokens: wire.Usage.Input + wire.Usage.Output}
	if wire.Status != "completed" || (len(wire.Error) > 0 && string(wire.Error) != "null") {
		return Response{Usage: usage}, failure("incomplete", "response did not complete")
	}
	r, err := parseItems(wire.Output)
	r.Usage = usage
	if err != nil {
		return r, err
	}
	r.Items = wire.Output
	return r, nil
}

func parseItems(items []json.RawMessage) (Response, error) {
	r := Response{}
	var texts, refusals []string
	for _, raw := range items {
		var item outputItem
		if json.Unmarshal(raw, &item) != nil {
			return Response{}, failure("invalid_response", "invalid output item")
		}
		if item.Status != "" && item.Status != "completed" {
			return Response{}, failure("incomplete", "output item did not complete")
		}
		switch item.Type {
		case "reasoning":
			// Only opaque replay uses these items; no reasoning is display text.
		case "message":
			if item.Role != "assistant" {
				return Response{}, failure("invalid_response", "unexpected output role")
			}
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					texts = append(texts, part.Text)
				case "refusal":
					refusals = append(refusals, part.Refusal)
				default:
					return Response{}, failure("invalid_response", "unsupported output content")
				}
			}
		case "function_call":
			call := Call{ID: item.CallID, Name: item.Name, Arguments: item.Arguments}
			if !validCall(call) {
				return Response{}, failure("invalid_response", "incomplete tool arguments")
			}
			r.Calls = append(r.Calls, call)
		default:
			return Response{}, failure("invalid_response", "unsupported output item")
		}
	}
	if len(refusals) > 0 {
		return Response{Refusal: strings.Join(refusals, "\n")}, failure("refusal", "provider declined the request")
	}
	r.Text = strings.Join(texts, "\n")
	if strings.TrimSpace(r.Text) == "" && len(r.Calls) == 0 {
		return Response{}, failure("empty", "no visible response or tool call")
	}
	return r, nil
}

func parseChat(body []byte) (Response, error) {
	var wire struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
				Refusal string `json:"refusal"`
				Calls   []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Input   int `json:"prompt_tokens"`
			Output  int `json:"completion_tokens"`
			Details struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &wire) != nil || len(wire.Choices) != 1 {
		return Response{}, failure("invalid_response", "expected one complete chat choice")
	}
	r := Response{Usage: Usage{InputTokens: wire.Usage.Input, OutputTokens: wire.Usage.Output, ReasoningTokens: wire.Usage.Details.Reasoning, TotalTokens: wire.Usage.Input + wire.Usage.Output}}
	choice := wire.Choices[0]
	if choice.Message.Refusal != "" {
		r.Refusal = choice.Message.Refusal
		return r, failure("refusal", "provider declined the request")
	}
	if choice.Finish != "stop" && choice.Finish != "tool_calls" {
		return r, failure("incomplete", "chat response did not complete")
	}
	if choice.Message.Role != "assistant" {
		return r, failure("invalid_response", "unexpected chat role")
	}
	for _, call := range choice.Message.Calls {
		c := Call{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments}
		if call.Type != "function" || !validCall(c) {
			return Response{Usage: r.Usage}, failure("invalid_response", "incomplete tool arguments")
		}
		r.Calls = append(r.Calls, c)
	}
	r.Text = choice.Message.Content
	if strings.TrimSpace(r.Text) == "" && len(r.Calls) == 0 {
		return r, failure("empty", "no visible response or tool call")
	}
	return r, nil
}
