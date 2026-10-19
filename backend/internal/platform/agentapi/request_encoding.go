package agentapi

import (
	"encoding/json"
	"errors"
)

const maxChatRequestBytes = 16 << 20

var errChatRequestTooLarge = errors.New("请求体超过大小限制")

// chatRequestEncoder 在单次工具循环内缓存只追加的消息历史。
// 每次请求仍携带完整历史，已编码的消息不再重复转义。
type chatRequestEncoder struct {
	messages     []json.RawMessage
	messageBytes int
}

func (encoder *chatRequestEncoder) encode(payload chatRequest) ([]byte, error) {
	model, err := json.Marshal(payload.Model)
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(struct {
		Tools       []chatTool `json:"tools"`
		Temperature *float64   `json:"temperature,omitempty"`
	}{payload.Tools, payload.Temperature})
	if err != nil {
		return nil, err
	}
	if len(payload.Messages) < len(encoder.messages) {
		encoder.messages = nil
		encoder.messageBytes = 0
	}
	for index := len(encoder.messages); index < len(payload.Messages); index++ {
		encoded, err := json.Marshal(payload.Messages[index])
		if err != nil {
			return nil, err
		}
		if len(encoded) > maxChatRequestBytes-encoder.messageBytes {
			return nil, errChatRequestTooLarge
		}
		encoder.messages = append(encoder.messages, encoded)
		encoder.messageBytes += len(encoded)
	}
	messagesSize := 2 + encoder.messageBytes + max(0, len(encoder.messages)-1)
	if payload.Messages == nil {
		messagesSize = len("null")
	}
	requestSize := len(`{"model":`) + len(model) + len(`,"messages":`) + messagesSize + len(metadata)
	if requestSize > maxChatRequestBytes {
		return nil, errChatRequestTooLarge
	}
	encoded := make([]byte, 0, requestSize)
	encoded = append(encoded, `{"model":`...)
	encoded = append(encoded, model...)
	encoded = append(encoded, `,"messages":`...)
	if payload.Messages == nil {
		encoded = append(encoded, "null"...)
	} else {
		encoded = append(encoded, '[')
		for index, message := range encoder.messages {
			if index != 0 {
				encoded = append(encoded, ',')
			}
			encoded = append(encoded, message...)
		}
		encoded = append(encoded, ']')
	}
	encoded = append(encoded, ',')
	// metadata 已经过 JSON 编码；其右花括号同时结束整个请求。
	encoded = append(encoded, metadata[1:]...)
	return encoded, nil
}
