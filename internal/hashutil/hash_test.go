package hashutil

import "testing"

func TestPromptHash_Deterministic(t *testing.T) {
	hash1 := PromptHash("ns", "tell me a joke", "gpt-4")
	hash2 := PromptHash("ns", "tell me a joke", "gpt-4")
	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for identical inputs, got %x and %x", hash1, hash2)
	}
}

func TestPromptHash_DifferentNamespaces(t *testing.T) {
	hash1 := PromptHash("namespace-a", "same prompt", "same-model")
	hash2 := PromptHash("namespace-b", "same prompt", "same-model")
	if hash1 == hash2 {
		t.Fatalf("expected different hashes for different namespaces, both were %x", hash1)
	}
}
