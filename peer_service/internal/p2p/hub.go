package p2p

import (
	"sync"

	clientpb "peer_service/proto"
)

type subscriber struct {
	peerID string
	stream clientpb.P2PTClient_ControlStreamServer
}

type Hub struct {
	mu   sync.RWMutex
	subs map[string]subscriber
}

func NewHub() *Hub { return &Hub{subs: map[string]subscriber{}} }

func (h *Hub) Add(id string, s clientpb.P2PTClient_ControlStreamServer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[id] = subscriber{id, s}
}

func (h *Hub) Remove(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, id)
}

func (h *Hub) Broadcast(msg *clientpb.ServerMsg) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, sub := range h.subs {
		if err := sub.stream.Send(msg); err != nil {
			// best-effort; drop faulty stream
			go h.Remove(id)
		}
	}
}
