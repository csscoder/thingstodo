package sse

import (
	"encoding/json"
	"sync"
)

type Event struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type Broker struct {
	mu        sync.RWMutex
	clients   map[string]chan Event
	seqSource func() (int64, error)
}

func NewBroker() *Broker {
	return &Broker{
		clients: make(map[string]chan Event),
	}
}

func (b *Broker) Subscribe(clientID string) <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 64)
	b.clients[clientID] = ch
	return ch
}

func (b *Broker) Unsubscribe(clientID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.clients[clientID]; ok {
		close(ch)
		delete(b.clients, clientID)
	}
}

func (b *Broker) Broadcast(event Event, excludeClient string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, ch := range b.clients {
		if id == excludeClient {
			continue
		}
		select {
		case ch <- event:
		default:
			// Drop event if client is too slow.
		}
	}
}

func (b *Broker) BroadcastJSON(eventType string, data interface{}) {
	b.Broadcast(Event{Type: eventType, Data: data}, "")
}

// SetSeqSource wires the function the broker reads the latest change-log seq
// from at broadcast time. The router binds it to
// ChangeLogRepository.GetLatestSeq.
func (b *Broker) SetSeqSource(f func() (int64, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seqSource = f
}

// BroadcastChange sends a single-entity change event: data plus the entity,
// its id and — when the seq source is wired and readable — the latest
// change-log seq, read after the write that triggered the broadcast, so it
// is at least the write's own entry. A missing or erroring seq source omits
// seq instead of fabricating one. bulk_change, connected and reminder_fired
// keep their own shapes and do not go through this method.
func (b *Broker) BroadcastChange(eventType, entity, id string, data map[string]any) {
	payload := make(map[string]any, len(data)+3)
	for k, v := range data {
		payload[k] = v
	}
	payload["entity"] = entity
	payload["id"] = id
	b.mu.RLock()
	f := b.seqSource
	b.mu.RUnlock()
	if f != nil {
		if seq, err := f(); err == nil {
			payload["seq"] = seq
		}
	}
	b.BroadcastJSON(eventType, payload)
}

func FormatSSE(event Event) ([]byte, error) {
	data, err := json.Marshal(event.Data)
	if err != nil {
		return nil, err
	}
	var buf []byte
	buf = append(buf, "event: "...)
	buf = append(buf, event.Type...)
	buf = append(buf, '\n')
	buf = append(buf, "data: "...)
	buf = append(buf, data...)
	buf = append(buf, '\n', '\n')
	return buf, nil
}
