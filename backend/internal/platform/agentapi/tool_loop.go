package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const maxToolResultBytes = 64 << 10

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type ToolExecution func(context.Context, string, json.RawMessage) (json.RawMessage, error)
type SubmissionHandler func(string, json.RawMessage) (bool, error)

type ToolLoopRequest struct {
	SystemPrompt string
	UserPrompt   string
	Tools        []ToolDefinition
	MaxSteps     int
	Execute      ToolExecution
	Submit       SubmissionHandler
}

type ToolLoopStep struct {
	Sequence  int
	ToolName  string
	Arguments json.RawMessage
	Result    json.RawMessage
	Submitted bool
	ErrorCode string
}

type ToolLoopResult struct {
	Submission   json.RawMessage
	Steps        []ToolLoopStep
	InputTokens  int64
	OutputTokens int64
}

func (client *Client) RunToolLoop(ctx context.Context, request ToolLoopRequest) (ToolLoopResult, error) {
	if request.MaxSteps <= 0 || request.MaxSteps > 64 || request.Execute == nil || request.Submit == nil {
		return ToolLoopResult{}, &Error{Code: "agent_protocol_unsupported"}
	}
	tools := make([]chatTool, 0, len(request.Tools))
	known := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Name == "" || tool.Parameters == nil {
			return ToolLoopResult{}, &Error{Code: "agent_protocol_unsupported"}
		}
		if _, duplicate := known[tool.Name]; duplicate {
			return ToolLoopResult{}, &Error{Code: "agent_protocol_unsupported"}
		}
		known[tool.Name] = struct{}{}
		tools = append(tools, chatTool{Type: "function", Function: chatToolDefinition{
			Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters, Strict: true,
		}})
	}
	messages := []chatMessage{{Role: "system", Content: request.SystemPrompt}, {Role: "user", Content: request.UserPrompt}}
	result := ToolLoopResult{Steps: make([]ToolLoopStep, 0, request.MaxSteps)}
	sequence := 0
	invalidSubmissions := 0
	invalidToolCalls := 0
	var lastSubmissionError error
	for sequence < request.MaxSteps {
		response, err := client.complete(ctx, chatRequest{
			Model: client.model, Messages: messages, Tools: tools, Temperature: float64Pointer(0),
		})
		if err != nil {
			return result, err
		}
		result.InputTokens += response.Usage.PromptTokens
		result.OutputTokens += response.Usage.CompletionTokens
		if len(response.Choices) != 1 {
			return result, &Error{Code: "agent_response_invalid", Retryable: true}
		}
		assistant := response.Choices[0].Message
		messages = append(messages, assistant)
		if len(assistant.ToolCalls) == 0 {
			invalidSubmissions++
			sequence++
			lastSubmissionError = errors.New("agent response did not call a submission tool")
			toolResult := repairResult("agent_submission_missing")
			result.Steps = append(result.Steps, ToolLoopStep{
				Sequence: sequence, ToolName: "invalid_submission", Arguments: json.RawMessage(`{}`), Result: toolResult,
				ErrorCode: "agent_submission_missing",
			})
			messages = append(messages, chatMessage{Role: "user", Content: "The previous response was not accepted. Call one supplied tool and submit a typed result."})
			continue
		}
		for _, call := range assistant.ToolCalls {
			if sequence >= request.MaxSteps {
				return exhaustedToolLoop(result, invalidSubmissions, invalidToolCalls, lastSubmissionError)
			}
			if _, ok := known[call.Function.Name]; !ok {
				invalidToolCalls++
				sequence++
				toolResult := repairResult("agent_tool_unknown")
				result.Steps = append(result.Steps, ToolLoopStep{
					Sequence: sequence, ToolName: "invalid_tool_call", Arguments: json.RawMessage(`{}`), Result: toolResult,
					ErrorCode: "agent_tool_unknown",
				})
				messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: string(toolResult)})
				continue
			}
			arguments := json.RawMessage(call.Function.Arguments)
			if !json.Valid(arguments) {
				invalidToolCalls++
				sequence++
				toolResult := repairResult("agent_tool_arguments_invalid")
				result.Steps = append(result.Steps, ToolLoopStep{
					Sequence: sequence, ToolName: call.Function.Name, Arguments: json.RawMessage(`{}`), Result: toolResult,
					ErrorCode: "agent_tool_arguments_invalid",
				})
				messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: string(toolResult)})
				continue
			}
			sequence++
			step := ToolLoopStep{Sequence: sequence, ToolName: call.Function.Name, Arguments: arguments}
			submitted, submitErr := request.Submit(call.Function.Name, arguments)
			if submitErr != nil {
				invalidSubmissions++
				lastSubmissionError = submitErr
				step.ErrorCode = repairErrorCode(submitErr, "agent_submission_invalid")
				step.Result = repairResult(step.ErrorCode)
				result.Steps = append(result.Steps, step)
				messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: string(step.Result)})
				continue
			}
			if submitted {
				step.Submitted = true
				result.Submission = arguments
				result.Steps = append(result.Steps, step)
				return result, nil
			}
			toolResult, executeErr := request.Execute(ctx, call.Function.Name, arguments)
			if executeErr != nil {
				step.ErrorCode = "agent_tool_scope_violation"
				result.Steps = append(result.Steps, step)
				return result, &Error{Code: "agent_tool_scope_violation", Cause: executeErr}
			}
			if !json.Valid(toolResult) || len(toolResult) > maxToolResultBytes {
				step.ErrorCode = "agent_tool_result_invalid"
				result.Steps = append(result.Steps, step)
				return result, &Error{Code: "agent_tool_call_invalid", Retryable: true, Cause: fmt.Errorf("tool result is invalid")}
			}
			step.Result = toolResult
			result.Steps = append(result.Steps, step)
			messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: string(toolResult)})
		}
	}
	return exhaustedToolLoop(result, invalidSubmissions, invalidToolCalls, lastSubmissionError)
}

func exhaustedToolLoop(result ToolLoopResult, invalidSubmissions, invalidToolCalls int, lastSubmissionError error) (ToolLoopResult, error) {
	if invalidSubmissions > 0 {
		return result, &Error{Code: "agent_submission_exhausted", Retryable: true, Cause: lastSubmissionError}
	}
	if invalidToolCalls > 0 {
		return result, &Error{Code: "agent_tool_call_exhausted", Retryable: true}
	}
	return result, &Error{Code: "agent_submission_missing", Retryable: true}
}

func repairResult(code string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"accepted": false, "error": code})
	return encoded
}

func repairErrorCode(err error, fallback string) string {
	type repairCoder interface {
		RepairCode() string
	}
	var coded repairCoder
	if errors.As(err, &coded) {
		code := coded.RepairCode()
		if validRepairCode(code) {
			return code
		}
	}
	return fallback
}

func validRepairCode(code string) bool {
	if code == "" || len(code) > 96 {
		return false
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}
