package adapter

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// reasoningPayload is the self-contained representation of a Kimi thinking
// block that travels inside Responses reasoning.encrypted_content. Keeping
// both the thinking text and the signature makes the round-trip independent
// of how the client treats summaries.
type reasoningPayload struct {
	V         int    `json:"v"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Redacted  string `json:"redacted,omitempty"`
}

func encodeReasoning(thinking, signature string) string {
	b, _ := json.Marshal(reasoningPayload{V: 1, Thinking: thinking, Signature: signature})
	return base64.RawURLEncoding.EncodeToString(b)
}

func encodeRedactedReasoning(data string) string {
	b, _ := json.Marshal(reasoningPayload{V: 1, Redacted: data})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeReasoning recovers a Kimi thinking block from encrypted_content.
// Fallback: if the payload is not our envelope, treat the raw string as a
// bare signature and use the summary text as the thinking content, matching
// the simple {"encrypted_content": "<signature>"} convention.
//
// OpenAI/Codex "gAAAA..." blobs are foreign ciphertext: Anthropic-compatible
// upstreams reject them with 400, so they are never replayed (mirrors
// sub2api#5166).
func decodeReasoning(encryptedContent, summaryText string) (payload reasoningPayload, ok bool) {
	if encryptedContent != "" {
		if raw, err := base64.RawURLEncoding.DecodeString(encryptedContent); err == nil {
			var p reasoningPayload
			if err := json.Unmarshal(raw, &p); err == nil && p.V == 1 {
				return p, true
			}
		}
		if strings.HasPrefix(encryptedContent, "gAAAA") {
			return reasoningPayload{}, false
		}
		// Bare signature fallback.
		return reasoningPayload{V: 1, Thinking: summaryText, Signature: encryptedContent}, true
	}
	return reasoningPayload{}, false
}
