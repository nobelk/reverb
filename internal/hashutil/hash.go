package hashutil

import "crypto/sha256"

// PromptHash computes the SHA-256 hash of namespace + normalized_prompt + model_id.
// This is used as the key for exact-match cache lookups.
func PromptHash(namespace, normalizedPrompt, modelID string) [32]byte {
	h := sha256.New()
	h.Write([]byte(namespace))
	h.Write([]byte{0}) // separator
	h.Write([]byte(normalizedPrompt))
	h.Write([]byte{0}) // separator
	h.Write([]byte(modelID))
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
