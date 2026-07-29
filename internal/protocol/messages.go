package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// Wire message types exchanged as JSON over tcpduplex before file transfer.
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
	KeyID      string `json:"key_id"` // sha256 hex of shared key (identity)
}

type HelloOK struct {
	ServerTime time.Time `json:"server_time"`
	Message    string    `json:"message,omitempty"`
}

type HelloFail struct {
	Reason string `json:"reason"`
}

// PlanReq asks the host which backup mode to run.
type PlanReq struct {
	WantMode   string `json:"want_mode"` // auto|full|incremental
	SourceRoot string `json:"source_root"`
}

// FileEntry is one path in a backup manifest (relative to source root).
type FileEntry struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	Mode    uint32    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
	SHA256  string    `json:"sha256,omitempty"`
}

// Plan tells the client what to pack.
type Plan struct {
	Mode           string      `json:"mode"`
	BaseBackupID   string      `json:"base_backup_id,omitempty"`
	LastManifest   []FileEntry `json:"last_manifest,omitempty"`
	CompressPrefer string      `json:"compress_prefer"` // zstd|gzip
}

// BackupOffer announces an archive about to be transferred.
type BackupOffer struct {
	BackupID     string   `json:"backup_id"`
	Mode         string   `json:"mode"`
	BaseBackupID string   `json:"base_backup_id,omitempty"`
	Compress     string   `json:"compress"`
	ArchiveName  string   `json:"archive_name"`
	ArchiveSize  int64    `json:"archive_size"`
	FileCount    int      `json:"file_count"`
	Deleted      []string `json:"deleted,omitempty"`
	Hostname     string   `json:"hostname"`
	SourceRoot   string   `json:"source_root"`
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

type ErrorMsg struct {
	Reason string `json:"reason"`
}
