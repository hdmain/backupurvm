package host

import (
	"sync"
	"time"
)

// TaskStatus for live / completed backup jobs shown in the panel.
type TaskStatus string

const (
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
)

// Task is one backup receive job.
type Task struct {
	ID         string
	ClientName string
	Hostname   string
	Mode       string
	SourceRoot string
	Status     TaskStatus
	BytesDone  int64
	BytesTotal int64
	Message    string
	StartedAt  time.Time
	FinishedAt time.Time
}

// TaskHub tracks in-flight and last-completed backup tasks for the TUI.
type TaskHub struct {
	mu        sync.RWMutex
	running   map[string]*Task
	lastDone  *Task
	seq       uint64
}

func NewTaskHub() *TaskHub {
	return &TaskHub{running: make(map[string]*Task)}
}

func (h *TaskHub) Start(clientName, hostname, mode, sourceRoot, backupID string, total int64) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	id := backupID
	if id == "" {
		id = time.Now().UTC().Format("20060102-150405") + "-job"
	}
	t := &Task{
		ID:         id,
		ClientName: clientName,
		Hostname:   hostname,
		Mode:       mode,
		SourceRoot: sourceRoot,
		Status:     TaskRunning,
		BytesTotal: total,
		StartedAt:  time.Now().UTC(),
		Message:    "receiving",
	}
	h.running[id] = t
	return id
}

func (h *TaskHub) Progress(id string, done, total int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.running[id]
	if !ok {
		return
	}
	t.BytesDone = done
	if total > 0 {
		t.BytesTotal = total
	}
	t.Message = "transferring"
}

func (h *TaskHub) Complete(id string, bytes int64, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.running[id]
	if !ok {
		t = &Task{ID: id, StartedAt: time.Now().UTC()}
	} else {
		delete(h.running, id)
	}
	t.Status = TaskCompleted
	t.BytesDone = bytes
	if bytes > 0 {
		t.BytesTotal = bytes
	}
	t.FinishedAt = time.Now().UTC()
	if msg != "" {
		t.Message = msg
	} else {
		t.Message = "ok"
	}
	cp := *t
	h.lastDone = &cp
}

func (h *TaskHub) Fail(id string, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.running[id]
	if !ok {
		t = &Task{ID: id, StartedAt: time.Now().UTC()}
	} else {
		delete(h.running, id)
	}
	t.Status = TaskFailed
	t.FinishedAt = time.Now().UTC()
	t.Message = reason
	cp := *t
	h.lastDone = &cp
}

func (h *TaskHub) Running() []Task {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Task, 0, len(h.running))
	for _, t := range h.running {
		out = append(out, *t)
	}
	return out
}

func (h *TaskHub) LastCompleted() *Task {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.lastDone == nil {
		return nil
	}
	cp := *h.lastDone
	return &cp
}
