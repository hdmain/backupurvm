package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// Wire message types exchanged as JSON over tcpduplex.
const (
	TypeHello       = "hello"
	TypeHelloOK     = "hello_ok"
	TypeHelloFail   = "hello_fail"
	TypePlanReq     = "plan_req"
	TypePlan        = "plan"
	TypeBackupOffer = "backup_offer"
	TypeBackupReady = "backup_ready"
	TypeBackupDone  = "backup_done"
	TypeBackupFail  = "backup_fail"
	TypeCommand     = "command"
	TypeCommandAck  = "command_ack"
	TypeStatus      = "status"
	TypeHeartbeat   = "heartbeat"
	TypeError       = "error"
)

const (
	ModeFull        = "full"
	ModeIncremental = "incremental"
	ModeAuto        = "auto"
)

const (
	CompressZstd = "zstd"
	CompressGzip = "gzip"
)

// Session roles.
const (
	RoleAgent   = "agent"   // long-running client waiting for host commands
	RoleOneshot = "oneshot" // single backup then disconnect
)

// Host → client command names.
const (
	CmdBackupAuto = "backup_auto"
	CmdBackupFull = "backup_full"
	CmdBackupIncr = "backup_incremental"
	CmdStatus     = "status"
	CmdPing       = "ping"
)

// Envelope wraps every control-plane message.
type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func Encode(typ string, payload any) ([]byte, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(Envelope{Type: typ, Payload: raw})
}

func Decode(data []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return env, fmt.Errorf("protocol: decode: %w", err)
	}
	if env.Type == "" {
		return env, fmt.Errorf("protocol: missing type")
	}
	return env, nil
}

func DecodePayload[T any](env Envelope) (T, error) {
	var v T
	if len(env.Payload) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(env.Payload, &v); err != nil {
		return v, fmt.Errorf("protocol: payload %s: %w", env.Type, err)
	}
	return v, nil
}

// Hello is sent by the client after the encrypted session is up.
type Hello struct {
	ClientName string `json:"client_name"`
	Hostname   string `json:"hostname"`
	Version    string `json:"version"`
	KeyID      string `json:"key_id"`
	Role       string `json:"role,omitempty"` // agent|oneshot
	SourceRoot string `json:"source_root,omitempty"`
}

type HelloOK struct {
	ServerTime time.Time `json:"server_time"`
	Message    string    `json:"message,omitempty"`
	ClientID   string    `json:"client_id,omitempty"`
}

type HelloFail struct {
	Reason string `json:"reason"`
}

type PlanReq struct {
	WantMode   string `json:"want_mode"`
	SourceRoot string `json:"source_root"`
}

type FileEntry struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	Mode    uint32    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
	SHA256  string    `json:"sha256,omitempty"`
}

type Plan struct {
	Mode           string      `json:"mode"`
	BaseBackupID   string      `json:"base_backup_id,omitempty"`
	LastManifest   []FileEntry `json:"last_manifest,omitempty"` // legacy small manifests
	ManifestZstd   []byte      `json:"manifest_zstd,omitempty"` // preferred: zstd(JSON FileEntry[])
	CompressPrefer string      `json:"compress_prefer"`
}

type BackupOffer struct {
	BackupID     string    `json:"backup_id"`
	Mode         string    `json:"mode"`
	BaseBackupID string    `json:"base_backup_id,omitempty"`
	Compress     string    `json:"compress"`
	ArchiveName  string    `json:"archive_name"`
	ArchiveSize  int64     `json:"archive_size"`
	FileCount    int       `json:"file_count"`
	Deleted      []string  `json:"deleted,omitempty"`
	Hostname     string    `json:"hostname"`
	SourceRoot   string    `json:"source_root"`
	CreatedAt    time.Time `json:"created_at"`
}

type BackupReady struct {
	DestName string `json:"dest_name"`
}

type BackupDone struct {
	BackupID string `json:"backup_id"`
	StoredAs string `json:"stored_as"`
	Bytes    int64  `json:"bytes"`
}

type BackupFail struct {
	Reason string `json:"reason"`
}

// Command is sent by the host to a connected agent.
type Command struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	WantMode string `json:"want_mode,omitempty"` // for backup_* aliases
}

type CommandAck struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type StatusReport struct {
	ClientName string    `json:"client_name"`
	Hostname   string    `json:"hostname"`
	SourceRoot string    `json:"source_root"`
	Busy       bool      `json:"busy"`
	UptimeSec  int64     `json:"uptime_sec"`
	LastBackup time.Time `json:"last_backup,omitempty"`
	Message    string    `json:"message,omitempty"`
}

type Heartbeat struct {
	At time.Time `json:"at"`
}

type ErrorMsg struct {
	Reason string `json:"reason"`
}
