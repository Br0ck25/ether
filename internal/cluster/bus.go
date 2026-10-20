package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"
)

// maxNotify is PostgreSQL's NOTIFY payload limit (it must be shorter than
// 8000 bytes).
const maxNotify = 7999

// localBus fans events out to in-process subscribers.
type localBus struct {
	log  *slog.Logger
	mu   sync.RWMutex
	next int
	subs map[string]map[int]func([]byte)
}

func newLocalBus(log *slog.Logger) *localBus {
	return &localBus{log: log, subs: map[string]map[int]func([]byte){}}
}

func (b *localBus) subscribe(topic string, fn func([]byte)) func() {
	if fn == nil {
		return func() {}
	}
	b.mu.Lock()
	b.next++
	id := b.next
	if b.subs[topic] == nil {
		b.subs[topic] = map[int]func([]byte){}
	}
	b.subs[topic][id] = fn
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs[topic], id)
			if len(b.subs[topic]) == 0 {
				delete(b.subs, topic)
			}
			b.mu.Unlock()
		})
	}
}

// deliver calls every subscriber of topic in turn; a panicking subscriber
// is logged and does not stop the others.
func (b *localBus) deliver(topic string, payload []byte) {
	b.mu.RLock()
	fns := make([]func([]byte), 0, len(b.subs[topic]))
	for _, fn := range b.subs[topic] {
		fns = append(fns, fn)
	}
	b.mu.RUnlock()
	for _, fn := range fns {
		func() {
			defer func() {
				if r := recover(); r != nil {
					b.log.Error("cluster: event subscriber panicked", "topic", topic, "panic", r)
				}
			}()
			fn(payload)
		}()
	}
}

// envelope is the NOTIFY payload. Text payloads travel as a JSON string;
// anything that is not valid UTF-8 (or contains NUL, which NOTIFY rejects)
// travels base64-encoded.
type envelope struct {
	Topic string  `json:"t"`
	From  string  `json:"f"`           // sender node ID
	Inc   string  `json:"i"`           // sender incarnation (process)
	Text  *string `json:"p,omitempty"` // UTF-8 payload
	Bin   []byte  `json:"b,omitempty"` // other payloads (base64 in JSON)
}

func validTopic(topic string) bool {
	return topic != "" && len(topic) <= maxTopic && utf8.ValidString(topic) && !strings.ContainsRune(topic, 0)
}

// encodeEnvelope validates topic and payload and returns the NOTIFY text.
func encodeEnvelope(topic, from, inc string, payload []byte) (string, error) {
	if !validTopic(topic) {
		return "", fmt.Errorf("%w %q", ErrBadTopic, topic)
	}
	if len(payload) > MaxPayload {
		return "", fmt.Errorf("%w: %d bytes", ErrPayloadTooLarge, len(payload))
	}
	e := envelope{Topic: topic, From: from, Inc: inc}
	if len(payload) > 0 {
		if utf8.Valid(payload) && bytes.IndexByte(payload, 0) < 0 {
			s := string(payload)
			e.Text = &s
		} else {
			e.Bin = payload
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return "", err
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	if len(out) > maxNotify {
		return "", fmt.Errorf("%w: encoded event is %d bytes", ErrPayloadTooLarge, len(out))
	}
	return out, nil
}

func decodeEnvelope(s string) (envelope, []byte, error) {
	var e envelope
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		return e, nil, err
	}
	if !validTopic(e.Topic) {
		return e, nil, ErrBadTopic
	}
	switch {
	case e.Text != nil:
		return e, []byte(*e.Text), nil
	case e.Bin != nil:
		return e, e.Bin, nil
	}
	return e, nil, nil
}
