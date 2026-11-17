package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// MaxToolResultBytes 是单次工具结果的 JSON 字节上限。
	MaxToolResultBytes = 64 << 10
	// MaxToolResultTotalBytes 限制一次工具循环累计注入上下文的工具结果字节数，
	// 与步数上限共同约束上游请求体积和调用成本。
	MaxToolResultTotalBytes = 2 << 20
	// minimumToolStepTime 是发起下一次模型请求需要的最小剩余作业时间。
	// 作业总时限由调度方按步数预算计算，剩余时间不足时提前结束而不是
	// 等 River 硬截止后从第一轮重跑整个循环。
	minimumToolStepTime = 2 * time.Second
)

// ToolErrorKind 描述工具执行失败的可恢复语义。
type ToolErrorKind uint8

const (
	toolErrorInvalidArguments ToolErrorKind = iota + 1
	toolErrorScope
	toolErrorUnavailable
)

// ToolExecutionError 让工具执行器显式声明失败类别，避免把参数错误或
// 瞬态依赖故障一律当作越权终止。未分类的执行器错误按暂时不可用处理。
type ToolExecutionError struct {
	Kind  ToolErrorKind
	Cause error
}

func (err *ToolExecutionError) Error() string {
	return "tool execution error"
}

func (err *ToolExecutionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// InvalidToolArguments 标记模型可自行修正的参数或调用错误。
func InvalidToolArguments(cause error) error {
	return &ToolExecutionError{Kind: toolErrorInvalidArguments, Cause: cause}
}

// ToolScopeViolation 标记调用超出本次决议授权范围的越权错误。
func ToolScopeViolation(cause error) error {
	return &ToolExecutionError{Kind: toolErrorScope, Cause: cause}
}

// ToolTemporarilyUnavailable 标记执行依赖暂时不可用、可在稍后重试的错误。
func ToolTemporarilyUnavailable(cause error) error {
	return &ToolExecutionError{Kind: toolErrorUnavailable, Cause: cause}
}

func toolErrorKind(err error) ToolErrorKind {
	var executionErr *ToolExecutionError
	if errors.As(err, &executionErr) {
		return executionErr.Kind
	}
	return toolErrorUnavailable
}

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
		parameters, strict := prepareToolParameters(tool.Parameters)
		tools = append(tools, chatTool{Type: "function", Function: chatToolDefinition{
			Name: tool.Name, Description: tool.Description, Parameters: parameters, Strict: strict,
		}})
	}
	messages := make([]chatMessage, 0, 2+2*request.MaxSteps)
	messages = append(messages, chatMessage{Role: "system", Content: request.SystemPrompt}, chatMessage{Role: "user", Content: request.UserPrompt})
	result := ToolLoopResult{Steps: make([]ToolLoopStep, 0, request.MaxSteps)}
	encoder := chatRequestEncoder{messages: make([]json.RawMessage, 0, 2+2*request.MaxSteps)}
	sequence := 0
	invalidSubmissions := 0
	invalidToolCalls := 0
	toolResultBytes := 0
	var lastSubmissionError error
	for sequence < request.MaxSteps {
		if err := ctx.Err(); err != nil {
			return result, requestError(ctx, err)
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < minimumToolStepTime {
			// 作业预算不足以完成下一次往返，提前收敛；从第一轮重跑无法
			// 解决确定性的时间预算不足，因此不标记为可重试。
			return result, &Error{Code: "agent_budget_exhausted"}
		}
		response, err := client.completeWithEncoder(ctx, chatRequest{
			Model: client.model, Messages: messages, Tools: tools, Temperature: float64Pointer(0),
		}, &encoder)
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
			if err := ctx.Err(); err != nil {
				return result, requestError(ctx, err)
			}
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
			trimmed := bytes.TrimSpace(arguments)
			if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(arguments) {
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
			if ctx.Err() != nil || errors.Is(submitErr, context.Canceled) || errors.Is(submitErr, context.DeadlineExceeded) {
				err := requestError(ctx, submitErr)
				step.ErrorCode = err.Code
				result.Steps = append(result.Steps, step)
				return result, err
			}
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
			if ctx.Err() != nil || errors.Is(executeErr, context.Canceled) || errors.Is(executeErr, context.DeadlineExceeded) {
				err := requestError(ctx, executeErr)
				step.ErrorCode = err.Code
				result.Steps = append(result.Steps, step)
				return result, err
			}
			if executeErr != nil {
				switch toolErrorKind(executeErr) {
				case toolErrorInvalidArguments:
					// 参数/调用错误消耗一步并把修复提示交还模型，与未知工具、
					// 非法 JSON 参数使用同一修复通道。
					invalidToolCalls++
					step.ErrorCode = "agent_tool_arguments_invalid"
					step.Result = repairResult(step.ErrorCode)
					result.Steps = append(result.Steps, step)
					messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: string(step.Result)})
					continue
				case toolErrorScope:
					step.ErrorCode = "agent_tool_scope_violation"
					result.Steps = append(result.Steps, step)
					return result, &Error{Code: "agent_tool_scope_violation", Cause: executeErr}
				default:
					// 瞬态依赖故障保留可重试语义，由 River 重试而不是让模型
					// 在依赖不可用时反复调用工具。
					step.ErrorCode = "agent_tool_execution_failed"
					result.Steps = append(result.Steps, step)
					return result, &Error{Code: "agent_tool_execution_failed", Retryable: true, Cause: executeErr}
				}
			}
			if !json.Valid(toolResult) {
				step.ErrorCode = "agent_tool_result_invalid"
				result.Steps = append(result.Steps, step)
				return result, &Error{Code: "agent_tool_call_invalid", Retryable: true, Cause: fmt.Errorf("tool result is invalid")}
			}
			if len(toolResult) > MaxToolResultBytes {
				// 单次结果超限时降级为修复提示；工具层已尽量分页或截断，
				// 这里保证循环不会因单个条目直接永久失败。
				invalidToolCalls++
				step.ErrorCode = "agent_tool_result_too_large"
				step.Result = repairResult(step.ErrorCode)
				result.Steps = append(result.Steps, step)
				messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: string(step.Result)})
				continue
			}
			toolResultBytes += len(toolResult)
			if toolResultBytes > MaxToolResultTotalBytes {
				invalidToolCalls++
				step.ErrorCode = "agent_tool_result_budget_exhausted"
				step.Result = repairResult(step.ErrorCode)
				result.Steps = append(result.Steps, step)
				messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: string(step.Result)})
				continue
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
