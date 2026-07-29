package host

import (
	"fmt"
	"sync"
	"time"

	"github.com/hdmain/backupurvm/internal/protocol"
	"github.com/hdmain/tcpduplex"
)

// AgentPeer is a connected long-running client.
type AgentPeer struct {
	ID         string
	Name       string
	Hostname   string
	SourceRoot string
	Connected  time.Time
	LastSeen   time.Time
	Busy       bool
	Online     bool

	conn   *tcpduplex.Conn
	cmdCh  chan protocol.Command
	cancel func()
}

// PeerHub tracks online agents and delivers host commands.
type PeerHub struct {
	mu    sync.RWMutex
	peers map[string]*AgentPeer
}

func NewPeerHub() *PeerHub {
	return &PeerHub{peers: make(map[string]*AgentPeer)}
}

func (h *PeerHub) Register(p *AgentPeer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.peers[p.ID]; ok && old.cancel != nil && old != p {
		old.cancel()
	}
	p.Online = true
	p.Connected = time.Now().UTC()
	p.LastSeen = p.Connected
	if p.cmdCh == nil {
		p.cmdCh = make(chan protocol.Command, 8)
	}
	h.peers[p.ID] = p
}

func (h *PeerHub) Unregister(id string, p *AgentPeer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, ok := h.peers[id]
	if !ok || cur != p {
		return
	}
	cur.Online = false
	delete(h.peers, id)
}

func (h *PeerHub) Touch(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.peers[id]; ok {
		p.LastSeen = time.Now().UTC()
	}
}

func (h *PeerHub) SetBusy(id string, busy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.peers[id]; ok {
		p.Busy = busy
	}
}

func (h *PeerHub) List() []AgentPeer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]AgentPeer, 0, len(h.peers))
	for _, p := range h.peers {
		cp := *p
		cp.conn = nil
		cp.cmdCh = nil
		cp.cancel = nil
		out = append(out, cp)
	}
	return out
}

func (h *PeerHub) OnlineIDs() map[string]AgentPeer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]AgentPeer, len(h.peers))
	for id, p := range h.peers {
		cp := *p
		cp.conn = nil
		cp.cmdCh = nil
		cp.cancel = nil
		out[id] = cp
	}
	return out
}

// SendCommand queues a command for an online agent.
func (h *PeerHub) SendCommand(clientID, name string) error {
	h.mu.RLock()
	p, ok := h.peers[clientID]
	h.mu.RUnlock()
	if !ok || !p.Online {
		return fmt.Errorf("server offline")
	}
	if p.Busy {
		return fmt.Errorf("server busy")
	}
	cmd := protocol.Command{
		ID:   fmt.Sprintf("%d", time.Now().UnixNano()),
		Name: name,
	}
	select {
	case p.cmdCh <- cmd:
		return nil
	default:
		return fmt.Errorf("command queue full")
	}
}

func (h *PeerHub) SendCommandByName(nameOrHost, cmdName string) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, p := range h.peers {
		if p.Name == nameOrHost || p.Hostname == nameOrHost || p.ID == nameOrHost {
			if !p.Online {
				return fmt.Errorf("server offline")
			}
			if p.Busy {
				return fmt.Errorf("server busy")
			}
			cmd := protocol.Command{
				ID:   fmt.Sprintf("%d", time.Now().UnixNano()),
				Name: cmdName,
			}
			select {
			case p.cmdCh <- cmd:
				return nil
			default:
				return fmt.Errorf("command queue full")
			}
		}
	}
	return fmt.Errorf("server not connected")
}

// BroadcastCommand sends cmdName to every online, non-busy agent.
// Returns how many peers accepted the command.
func (h *PeerHub) BroadcastCommand(cmdName string) (sent int, skipped int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, p := range h.peers {
		if !p.Online {
			continue
		}
		if p.Busy {
			skipped++
			continue
		}
		cmd := protocol.Command{
			ID:   fmt.Sprintf("%d", time.Now().UnixNano()),
			Name: cmdName,
		}
		select {
		case p.cmdCh <- cmd:
			sent++
		default:
			skipped++
		}
	}
	return sent, skipped
}
