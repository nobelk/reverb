package nats

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	natsclient "github.com/nats-io/nats.go"

	"github.com/org/reverb/pkg/cdc"
)

// defaultSubject is the NATS subject subscribed to if none is specified.
const defaultSubject = "reverb.sources.changed"

// Compile-time check that Listener implements cdc.Listener.
var _ cdc.Listener = (*Listener)(nil)

// Listener implements cdc.Listener by subscribing to a NATS JetStream subject.
type Listener struct {
	url     string
	subject string
	log     *slog.Logger
}

// natsPayload is the JSON structure expected on the NATS subject.
type natsPayload struct {
	SourceID    string `json:"source_id"`
	ContentHash string `json:"content_hash"`
	Timestamp   string `json:"timestamp"`
}

// New creates a new NATS Listener. If logger is nil, slog.Default() is used.
func New(url, subject string, logger *slog.Logger) (*Listener, error) {
	if url == "" {
		return nil, fmt.Errorf("nats: url is required")
	}
	if subject == "" {
		subject = defaultSubject
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Listener{
		url:     url,
		subject: subject,
		log:     logger,
	}, nil
}

// Name returns the listener name.
func (l *Listener) Name() string {
	return "nats"
}

// Start connects to NATS, subscribes to the configured subject, and forwards
// decoded ChangeEvents to the events channel. It blocks until the context is
// canceled, then drains and closes the connection.
func (l *Listener) Start(ctx context.Context, events chan<- cdc.ChangeEvent) error {
	nc, err := natsclient.Connect(l.url)
	if err != nil {
		return fmt.Errorf("nats: connect to %s: %w", l.url, err)
	}
	defer nc.Drain()

	sub, err := nc.Subscribe(l.subject, func(msg *natsclient.Msg) {
		event, err := decodeMessage(msg.Data)
		if err != nil {
			l.log.Warn("nats: failed to decode message", "error", err)
			return
		}
		select {
		case events <- event:
		case <-ctx.Done():
		}
	})
	if err != nil {
		return fmt.Errorf("nats: subscribe to %s: %w", l.subject, err)
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
	return nil
}

// decodeMessage parses a raw NATS message payload into a ChangeEvent.
func decodeMessage(data []byte) (cdc.ChangeEvent, error) {
	var p natsPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return cdc.ChangeEvent{}, fmt.Errorf("nats: failed to decode message: %w", err)
	}

	if p.SourceID == "" {
		return cdc.ChangeEvent{}, fmt.Errorf("nats: message missing source_id")
	}

	event := cdc.ChangeEvent{
		SourceID:       p.SourceID,
		ContentHashHex: p.ContentHash,
	}

	if p.ContentHash != "" {
		decoded, err := hex.DecodeString(p.ContentHash)
		if err == nil && len(decoded) == 32 {
			copy(event.ContentHash[:], decoded)
		}
	}

	if p.Timestamp != "" {
		ts, err := time.Parse(time.RFC3339, p.Timestamp)
		if err == nil {
			event.Timestamp = ts
		} else {
			event.Timestamp = time.Now().UTC()
		}
	} else {
		event.Timestamp = time.Now().UTC()
	}

	return event, nil
}
