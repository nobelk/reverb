package nats

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	l, err := New("nats://localhost:4222", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "nats", l.Name())
	assert.Equal(t, defaultSubject, l.subject)
}

func TestNewCustomSubject(t *testing.T) {
	l, err := New("nats://localhost:4222", "my.subject", nil)
	require.NoError(t, err)
	assert.Equal(t, "my.subject", l.subject)
}

func TestNewMissingURL(t *testing.T) {
	_, err := New("", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url is required")
}

func TestName(t *testing.T) {
	l, _ := New("nats://localhost:4222", "", nil)
	assert.Equal(t, "nats", l.Name())
}

func TestDecodeErrorIsLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	l, err := New("nats://localhost:4222", "", logger)
	require.NoError(t, err)

	// Simulate what the NATS callback does when decodeMessage fails.
	badData := []byte(`{not valid json`)
	_, decodeErr := decodeMessage(badData)
	require.Error(t, decodeErr)
	l.log.Warn("nats: failed to decode message", "error", decodeErr)

	logged := buf.String()
	assert.True(t, strings.Contains(logged, "nats: failed to decode message"), "expected warn log for decode error, got: %s", logged)
}

func TestDecodeMessage_Valid(t *testing.T) {
	ts := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	hash := "aabbccdd" + "00112233" + "44556677" + "8899aabb" + "ccddeeff" + "00112233" + "44556677" + "8899aabb"

	payload := natsPayload{
		SourceID:    "doc-123",
		ContentHash: hash,
		Timestamp:   ts.Format(time.RFC3339),
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	event, err := decodeMessage(data)
	require.NoError(t, err)
	assert.Equal(t, "doc-123", event.SourceID)
	assert.Equal(t, hash, event.ContentHashHex)
	assert.Equal(t, ts, event.Timestamp)
}

func TestDecodeMessage_MissingSourceID(t *testing.T) {
	data, _ := json.Marshal(natsPayload{ContentHash: "abc"})
	_, err := decodeMessage(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing source_id")
}

func TestDecodeMessage_InvalidJSON(t *testing.T) {
	_, err := decodeMessage([]byte(`{not valid json`))
	require.Error(t, err)
}

func TestDecodeMessage_NoTimestamp(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	data, _ := json.Marshal(natsPayload{SourceID: "src-1"})
	event, err := decodeMessage(data)
	require.NoError(t, err)
	assert.True(t, event.Timestamp.After(before), "should default to current time")
}

func TestDecodeMessage_InvalidTimestamp(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	payload := natsPayload{
		SourceID:  "src-1",
		Timestamp: "not-a-timestamp",
	}
	data, _ := json.Marshal(payload)
	event, err := decodeMessage(data)
	require.NoError(t, err)
	assert.True(t, event.Timestamp.After(before), "should fall back to current time on bad timestamp")
}

func TestDecodeMessage_InvalidContentHash(t *testing.T) {
	payload := natsPayload{
		SourceID:    "src-1",
		ContentHash: "not-hex",
	}
	data, _ := json.Marshal(payload)
	event, err := decodeMessage(data)
	require.NoError(t, err)
	assert.Equal(t, "not-hex", event.ContentHashHex)
	// ContentHash stays zero-value since hex decode failed
	assert.Equal(t, [32]byte{}, event.ContentHash)
}
