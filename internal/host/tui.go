package host

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gliderlabs/ssh"
	"github.com/hdmain/backupurvm/internal/common"
	"github.com/hdmain/backupurvm/internal/protocol"
	"github.com/muesli/termenv"
)

// Panel tabs for the main list screens. Settings opens via S, not Tab.
const (
	tabOverview = 0
	tabClients  = 1
	tabSettings = 2
)

const (
	screenMain = iota
	screenClient
	screenHelp
	screenFind
	screenInput
)

type tickMsg time.Time

type downloadReadyMsg struct {
	url   string
	err   error
	label string // "selected" or "latest"
}

type tuiModel struct {
	panel *SSHPanel
	sess  ssh.Session

	width  int
	height int

	tab     int
	prevTab int // tab to restore when leaving Settings
	screen  int

	summaries    []ClientSummary
	archivedFrom int // first archived index in summaries; len(summaries) if none
	recent       []BackupRecord
	backups      []BackupRecord
	settings     []settingRow
	running      []Task
	lastDone     *Task

	overviewCursor int
	cursor         int
	offset         int

	selected ClientSummary
	status   string
	errMsg   string

	findTI    textinput.Model
	inputTI   textinput.Model
	inputKind string
}

type settingRow struct {
	Key      string
	Section  string
	Label    string
	Value    string
	Hint     string
	Editable bool
}

func newTUI(panel *SSHPanel, sess ssh.Session, width, height int) tuiModel {
	fi := textinput.New()
	fi.Placeholder = "name, host, or id…"
	fi.CharLimit = 64
	fi.Prompt = "Find client: "

	ii := textinput.New()
	ii.CharLimit = 512
	ii.Prompt = "> "

	m := tuiModel{
		panel:   panel,
		sess:    sess,
		width:   maxInt(width, 60),
		height:  maxInt(height, 12),
		tab:     tabOverview,
		screen:  screenMain,
		findTI:  fi,
		inputTI: ii,
		status:  "Ready.",
	}
	m.reload()
	return m
}

func (m *tuiModel) tabName() string {
	switch m.tab {
	case tabOverview:
		return "OVERVIEW"
	case tabClients:
		return "CLIENTS"
	case tabSettings:
		return "SETTINGS"
	default:
		return "?"
	}
}

func (m *tuiModel) reload() {
	cfg := m.panel.store.Get()
	sums, err := m.panel.storage.SummarizeClients()
	if err != nil {
		m.errMsg = err.Error()
		sums = nil
	} else {
		m.errMsg = ""
	}
	m.summaries = sums
	m.partitionSummaries()
	m.recent, _ = m.panel.storage.RecentBackups(12)
	m.running = m.panel.tasks.Running()
	m.lastDone = m.panel.tasks.LastCompleted()
	if m.lastDone == nil && len(m.recent) > 0 {
		r := m.recent[0]
		name := r.ClientName
		if name == "" {
			name = r.Hostname
		}
		m.lastDone = &Task{
			ID:         r.ID,
			ClientName: name,
			Hostname:   r.Hostname,
			Mode:       r.Mode,
			SourceRoot: r.SourceRoot,
			Status:     TaskCompleted,
			BytesDone:  r.Bytes,
			BytesTotal: r.Bytes,
			Message:    "ok",
			StartedAt:  r.CreatedAt,
			FinishedAt: r.CreatedAt,
		}
	}

	m.settings = []settingRow{
		{Section: "Backup", Key: "shared_key", Label: "Client shared key", Value: maskSecret(cfg.SharedKey), Hint: "PSK for --key / tcpduplex", Editable: true},
		{Section: "Backup", Key: "compress", Label: "Preferred compress", Value: cfg.CompressPrefer, Hint: "zstd or gzip", Editable: true},
		{Section: "Backup", Key: "max_backups", Label: "Keep backups / client", Value: fmt.Sprintf("%d", cfg.MaxBackupsPerClient), Hint: "0 = unlimited", Editable: true},
		{Section: "Backup", Key: "data_dir", Label: "Data directory", Value: cfg.DataDir, Editable: false},
		{Section: "Backup", Key: "key_id", Label: "Key fingerprint", Value: common.KeyID([]byte(cfg.SharedKey)), Editable: false},

		{Section: "Auto backup", Key: "auto_backup", Label: "Enabled", Value: onOff(cfg.AutoBackup), Hint: "on or off", Editable: true},
		{Section: "Auto backup", Key: "auto_backup_at", Label: "Schedule time", Value: scheduleTimeLabel(cfg.AutoBackupAt), Hint: "HH:MM local, or empty for any time", Editable: true},
		{Section: "Auto backup", Key: "auto_backup_every", Label: "Interval", Value: cfg.AutoBackupEvery, Hint: "e.g. 1h, 6h, 24h, 3d (min 1m)", Editable: true},
		{Section: "Auto backup", Key: "auto_backup_mode", Label: "Mode", Value: cfg.AutoBackupMode, Hint: "auto, full, or incremental", Editable: true},

		{Section: "Servers", Key: "archive_offline_after", Label: "Archive offline after", Value: archiveAfterLabel(cfg.ArchiveOfflineAfter), Hint: "e.g. 3d, 72h — empty/0 = never", Editable: true},

		{Section: "Network", Key: "listen_backup", Label: "Backup listen", Value: cfg.ListenBackup, Hint: "tcpduplex address", Editable: true},
		{Section: "Network", Key: "listen_ssh", Label: "SSH panel listen", Value: cfg.ListenSSH, Hint: "restart host to apply", Editable: false},

		{Section: "Access", Key: "ssh_password", Label: "SSH password", Value: maskSecret(cfg.SSHPassword), Hint: "empty disables password login", Editable: true},
		{Section: "Access", Key: "ssh_keys", Label: "Authorized keys", Value: fmt.Sprintf("%d", len(cfg.SSHAuthorizedKeys)), Editable: false},
		{Section: "Access", Key: "add_ssh_key", Label: "Add SSH public key…", Value: "", Editable: true},
		{Section: "Access", Key: "config_path", Label: "Config file", Value: m.panel.store.Path(), Editable: false},
	}

	switch m.tab {
	case tabOverview:
		m.status = fmt.Sprintf("%d server(s) · %d running · last %s", len(m.summaries), len(m.running), lastDoneLabel(m.lastDone))
		n := len(m.running) + len(m.summaries)
		if m.overviewCursor >= n && n > 0 {
			m.overviewCursor = n - 1
		}
	case tabClients:
		m.status = fmt.Sprintf("%d VPS client(s)", len(m.summaries))
		if m.cursor >= len(m.summaries) {
			m.cursor = maxInt(0, len(m.summaries)-1)
		}
	case tabSettings:
		m.status = "Enter edits · Esc back · all host options live here"
		if m.cursor >= len(m.settings) {
			m.cursor = maxInt(0, len(m.settings)-1)
		}
	}
}

func lastDoneLabel(t *Task) string {
	if t == nil {
		return "none"
	}
	name := t.ClientName
	if name == "" {
		name = t.Hostname
	}
	return name + "/" + t.Mode
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(tea.EnterAltScreen, tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = maxInt(msg.Width, 60)
		m.height = maxInt(msg.Height, 12)
		return m, nil
	case tickMsg:
		if m.screen == screenMain || m.screen == screenClient {
			prev, prevOv := m.cursor, m.overviewCursor
			m.reload()
			m.cursor, m.overviewCursor = prev, prevOv
			if m.screen == screenClient && m.selected.Client.ID != "" {
				recs, _ := m.panel.storage.ListBackups(m.selected.Client.ID)
				m.backups = recs
			}
		}
		return m, tickCmd()
	case downloadReadyMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.status = "Download failed"
			return m, nil
		}
		m.errMsg = ""
		m.status = "DOWNLOAD  " + msg.url
		m.panel.log.Printf("TUI: download %s ready %s", msg.label, msg.url)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenHelp:
		m.screen = screenMain
		return m, nil

	case screenFind:
		switch msg.String() {
		case "esc":
			m.screen = screenMain
			m.findTI.Blur()
			return m, nil
		case "enter":
			q := strings.ToLower(strings.TrimSpace(m.findTI.Value()))
			m.screen = screenMain
			m.tab = tabClients
			m.findTI.Blur()
			if q != "" {
				for i, s := range m.summaries {
					c := s.Client
					hay := strings.ToLower(c.Name + " " + c.Hostname + " " + c.ID)
					if strings.Contains(hay, q) {
						m.cursor = i
						m.ensureVisible(len(m.summaries))
						m.status = "Selected " + c.Name
						break
					}
				}
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.findTI, cmd = m.findTI.Update(msg)
			return m, cmd
		}

	case screenInput:
		switch msg.String() {
		case "esc":
			m.screen = screenMain
			m.inputTI.Blur()
			m.status = "Cancelled."
			return m, nil
		case "enter":
			val := m.inputTI.Value()
			m.screen = screenMain
			m.inputTI.Blur()
			if err := m.applyInput(val); err != nil {
				m.errMsg = err.Error()
			} else {
				m.errMsg = ""
				m.status = "Saved."
				m.reload()
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.inputTI, cmd = m.inputTI.Update(msg)
			return m, cmd
		}

	case screenClient:
		switch msg.String() {
		case "q", "esc", "backspace":
			m.screen = screenMain
			m.tab = tabClients
			m.status = "Back to clients."
		case "up", "k", "w":
			if m.cursor > 0 {
				m.cursor--
				m.ensureVisible(len(m.backups))
			}
		case "down", "j":
			if m.cursor < len(m.backups)-1 {
				m.cursor++
				m.ensureVisible(len(m.backups))
			}
		case "u":
			recs, err := m.panel.storage.ListBackups(m.selected.Client.ID)
			if err != nil {
				m.errMsg = err.Error()
			} else {
				m.backups = recs
				m.errMsg = ""
				m.status = fmt.Sprintf("%d backup(s) for %s", len(recs), m.selected.Client.Name)
			}
		case "?":
			m.screen = screenHelp
		case "s", "S":
			m.screen = screenMain
			m.prevTab = tabClients
			m.tab = tabSettings
			m.cursor = 0
			m.offset = 0
			m.reload()
		case "b":
			return m.sendSelectedCmd(protocol.CmdBackupAuto)
		case "B":
			return m.sendSelectedCmd(protocol.CmdBackupFull)
		case "i", "I":
			return m.sendSelectedCmd(protocol.CmdBackupIncr)
		case "p", "P":
			return m.sendSelectedCmd(protocol.CmdPing)
		case "d", "D":
			return m.toggleDownload()
		case "l", "L":
			return m.toggleDownloadLatest()
		case "tab":
			m.screen = screenMain
			m.tab = tabOverview
			m.reload()
		}
		return m, nil
	}

	// main screens
	switch msg.String() {
	case "q", "ctrl+c":
		if m.tab == tabSettings {
			m.tab = m.prevTab
			m.cursor = 0
			m.offset = 0
			m.reload()
			m.status = "Left settings."
			return m, nil
		}
		return m, tea.Quit
	case "esc":
		if m.tab == tabSettings {
			m.tab = m.prevTab
			m.cursor = 0
			m.offset = 0
			m.reload()
			m.status = "Left settings."
			return m, nil
		}
	case "?":
		m.screen = screenHelp
	case "s", "S":
		if m.tab != tabSettings {
			m.prevTab = m.tab
		}
		m.tab = tabSettings
		m.cursor = 0
		m.offset = 0
		m.reload()
		m.status = "Settings"
		return m, nil
	case "tab":
		if m.tab == tabSettings {
			m.tab = m.prevTab
		} else if m.tab == tabOverview {
			m.tab = tabClients
		} else {
			m.tab = tabOverview
		}
		m.cursor = 0
		m.overviewCursor = 0
		m.offset = 0
		m.reload()
	case "u":
		m.reload()
		m.status = "Refreshed."
	case "f":
		m.screen = screenFind
		m.findTI.SetValue("")
		m.findTI.Focus()
		return m, textinput.Blink
	case "up", "k", "w":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "enter", "e":
		return m.activate()
	case "b":
		return m.sendSelectedCmd(protocol.CmdBackupAuto)
	case "B":
		return m.sendSelectedCmd(protocol.CmdBackupFull)
	case "i", "I":
		return m.sendSelectedCmd(protocol.CmdBackupIncr)
	case "p", "P":
		return m.sendSelectedCmd(protocol.CmdPing)
	case "d", "D":
		return m.toggleDownload()
	case "l", "L":
		return m.toggleDownloadLatest()
	}
	return m, nil
}

func (m *tuiModel) nextTab() {
	if m.tab == tabOverview {
		m.tab = tabClients
	} else {
		m.tab = tabOverview
	}
}

func (m *tuiModel) moveCursor(delta int) {
	switch m.tab {
	case tabOverview:
		items := m.overviewItems()
		n := len(items)
		if n == 0 {
			return
		}
		m.overviewCursor = clamp(m.overviewCursor+delta, 0, n-1)
		m.ensureVisibleAt(m.overviewCursor, n)
	case tabClients:
		n := len(m.summaries)
		if n == 0 {
			return
		}
		m.cursor = clamp(m.cursor+delta, 0, n-1)
		m.ensureVisible(n)
	case tabSettings:
		n := len(m.settings)
		if n == 0 {
			return
		}
		m.cursor = clamp(m.cursor+delta, 0, n-1)
		m.ensureVisible(n)
	}
}

type overviewItem struct {
	kind string // running|server
	idx  int
}

func (m tuiModel) overviewItems() []overviewItem {
	var items []overviewItem
	for i := range m.running {
		items = append(items, overviewItem{kind: "running", idx: i})
	}
	for i := range m.summaries {
		items = append(items, overviewItem{kind: "server", idx: i})
	}
	return items
}

func (m tuiModel) activate() (tea.Model, tea.Cmd) {
	switch m.tab {
	case tabOverview:
		items := m.overviewItems()
		if len(items) == 0 {
			if m.lastDone != nil {
				return m.jumpToClientName(m.lastDone.ClientName, m.lastDone.Hostname)
			}
			m.status = "No servers yet."
			return m, nil
		}
		if m.overviewCursor < 0 || m.overviewCursor >= len(items) {
			return m, nil
		}
		it := items[m.overviewCursor]
		switch it.kind {
		case "running":
			t := m.running[it.idx]
			return m.jumpToClientName(t.ClientName, t.Hostname)
		case "server":
			m.cursor = it.idx
			m.tab = tabClients
			return m.openClient(m.summaries[it.idx])
		}
		return m, nil

	case tabClients:
		if len(m.summaries) == 0 {
			m.status = "No clients — wait for a VPS client to connect."
			return m, nil
		}
		return m.openClient(m.summaries[m.cursor])

	case tabSettings:
		if m.cursor < 0 || m.cursor >= len(m.settings) {
			return m, nil
		}
		row := m.settings[m.cursor]
		if !row.Editable {
			m.status = "Read-only."
			return m, nil
		}
		m.inputKind = row.Key
		m.inputTI.SetValue("")
		m.inputTI.Placeholder = row.Hint
		m.inputTI.EchoMode = textinput.EchoNormal
		if row.Key == "shared_key" || row.Key == "ssh_password" {
			m.inputTI.EchoMode = textinput.EchoPassword
		}
		cfg := m.panel.store.Get()
		switch row.Key {
		case "compress":
			m.inputTI.SetValue(cfg.CompressPrefer)
		case "max_backups":
			m.inputTI.SetValue(fmt.Sprintf("%d", cfg.MaxBackupsPerClient))
		case "listen_backup":
			m.inputTI.SetValue(cfg.ListenBackup)
		case "auto_backup":
			m.inputTI.SetValue(onOff(cfg.AutoBackup))
		case "auto_backup_at":
			m.inputTI.SetValue(cfg.AutoBackupAt)
		case "auto_backup_every":
			m.inputTI.SetValue(cfg.AutoBackupEvery)
		case "auto_backup_mode":
			m.inputTI.SetValue(cfg.AutoBackupMode)
		case "archive_offline_after":
			m.inputTI.SetValue(cfg.ArchiveOfflineAfter)
		}
		m.screen = screenInput
		m.inputTI.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m tuiModel) jumpToClientName(name, hostname string) (tea.Model, tea.Cmd) {
	for i, s := range m.summaries {
		if s.Client.Name == name || s.Client.Hostname == hostname {
			m.cursor = i
			m.tab = tabClients
			return m.openClient(s)
		}
	}
	m.status = "Client not found."
	return m, nil
}

func (m tuiModel) toggleDownload() (tea.Model, tea.Cmd) {
	if m.panel.downloads == nil {
		m.errMsg = "download server unavailable"
		return m, nil
	}
	if m.panel.downloads.Active() || m.panel.downloads.Building() {
		m.panel.downloads.Stop()
		m.errMsg = ""
		m.status = "Download server stopped."
		m.panel.log.Printf("TUI: download server stopped")
		return m, nil
	}
	if m.screen != screenClient || len(m.backups) == 0 {
		m.status = "Open a server, select an archive, then press D — or L for latest full."
		return m, nil
	}
	if m.cursor < 0 || m.cursor >= len(m.backups) {
		m.status = "Select a backup to download."
		return m, nil
	}
	rec := m.backups[m.cursor]
	url, started, err := m.panel.downloads.Toggle(rec)
	if err != nil {
		m.errMsg = err.Error()
		m.status = "Download failed"
		return m, nil
	}
	if !started {
		m.errMsg = ""
		m.status = "Download server stopped."
		return m, nil
	}
	m.errMsg = ""
	m.status = "DOWNLOAD  " + url
	m.panel.log.Printf("TUI: download ready %s", url)
	return m, nil
}

func (m tuiModel) toggleDownloadLatest() (tea.Model, tea.Cmd) {
	if m.panel.downloads == nil {
		m.errMsg = "download server unavailable"
		return m, nil
	}
	if m.panel.downloads.Active() || m.panel.downloads.Building() {
		m.panel.downloads.Stop()
		m.errMsg = ""
		m.status = "Download server stopped."
		return m, nil
	}
	if m.screen != screenClient || m.selected.Client.ID == "" {
		m.status = "Open a server (Enter), then press L to download latest full+incremental."
		return m, nil
	}
	clientID := m.selected.Client.ID
	compress := m.panel.store.Get().CompressPrefer
	m.status = "Building latest full archive (full + incrementals)…"
	m.errMsg = ""
	panel := m.panel
	return m, func() tea.Msg {
		recs, err := panel.storage.ListBackups(clientID)
		if err != nil {
			return downloadReadyMsg{err: err, label: "latest"}
		}
		url, started, err := panel.downloads.ToggleLatest(recs, compress)
		if err != nil {
			return downloadReadyMsg{err: err, label: "latest"}
		}
		if !started {
			return downloadReadyMsg{err: fmt.Errorf("download server stopped"), label: "latest"}
		}
		return downloadReadyMsg{url: url, label: "latest"}
	}
}

func (m tuiModel) sendSelectedCmd(cmdName string) (tea.Model, tea.Cmd) {
	id, label := m.selectedServerID()
	if id == "" {
		m.status = "Select an online server first."
		m.errMsg = ""
		return m, nil
	}
	if err := m.panel.peers.SendCommand(id, cmdName); err != nil {
		m.errMsg = err.Error()
		m.status = "Command failed: " + label
		return m, nil
	}
	m.errMsg = ""
	m.status = fmt.Sprintf("Sent %s → %s", cmdName, label)
	m.panel.log.Printf("TUI command %s → %s (%s)", cmdName, label, id)
	return m, nil
}

func (m tuiModel) selectedServerID() (id, label string) {
	switch {
	case m.screen == screenClient && m.selected.Client.ID != "":
		return m.selected.Client.ID, m.selected.Client.Name
	case m.tab == tabClients && m.cursor >= 0 && m.cursor < len(m.summaries):
		s := m.summaries[m.cursor]
		return s.Client.ID, s.Client.Name
	case m.tab == tabOverview:
		items := m.overviewItems()
		if m.overviewCursor >= 0 && m.overviewCursor < len(items) {
			it := items[m.overviewCursor]
			if it.kind == "server" {
				s := m.summaries[it.idx]
				return s.Client.ID, s.Client.Name
			}
			if it.kind == "running" {
				t := m.running[it.idx]
				for _, s := range m.summaries {
					if s.Client.Name == t.ClientName || s.Client.Hostname == t.Hostname {
						return s.Client.ID, s.Client.Name
					}
				}
			}
		}
	}
	// fallback: single online peer
	online := m.panel.peers.OnlineIDs()
	if len(online) == 1 {
		for id, p := range online {
			return id, p.Name
		}
	}
	return "", ""
}

func (m tuiModel) openClient(sum ClientSummary) (tea.Model, tea.Cmd) {
	recs, err := m.panel.storage.ListBackups(sum.Client.ID)
	if err != nil {
		m.errMsg = err.Error()
		return m, nil
	}
	m.selected = sum
	m.backups = recs
	m.cursor = 0
	m.offset = 0
	m.screen = screenClient
	m.status = fmt.Sprintf("%s — %d backup(s), %s stored",
		sum.Client.Name, sum.BackupCount, common.FormatBytes(sum.StoredBytes))
	return m, nil
}

func (m *tuiModel) applyInput(val string) error {
	switch m.inputKind {
	case "shared_key":
		val = strings.TrimSpace(val)
		if val == "" {
			return fmt.Errorf("shared key cannot be empty")
		}
		return m.panel.store.Update(func(c *Config) error {
			c.SharedKey = val
			return nil
		})
	case "ssh_password":
		return m.panel.store.Update(func(c *Config) error {
			c.SSHPassword = val
			return nil
		})
	case "compress":
		val = strings.ToLower(strings.TrimSpace(val))
		if val != "zstd" && val != "gzip" {
			return fmt.Errorf("use zstd or gzip")
		}
		return m.panel.store.Update(func(c *Config) error {
			c.CompressPrefer = val
			return nil
		})
	case "max_backups":
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(val), "%d", &n); err != nil || n < 0 {
			return fmt.Errorf("invalid number")
		}
		return m.panel.store.Update(func(c *Config) error {
			c.MaxBackupsPerClient = n
			return nil
		})
	case "listen_backup":
		val = strings.TrimSpace(val)
		if val == "" {
			return fmt.Errorf("address required")
		}
		return m.panel.store.Update(func(c *Config) error {
			c.ListenBackup = val
			return nil
		})
	case "auto_backup":
		on, err := parseOnOff(val)
		if err != nil {
			return err
		}
		return m.panel.store.Update(func(c *Config) error {
			c.AutoBackup = on
			return nil
		})
	case "auto_backup_at":
		val = strings.TrimSpace(val)
		if _, _, _, err := ParseClockHHMM(val); err != nil {
			return err
		}
		return m.panel.store.Update(func(c *Config) error {
			c.AutoBackupAt = val
			return nil
		})
	case "auto_backup_every":
		val = strings.TrimSpace(val)
		d, err := ParseFlexibleDuration(val)
		if err != nil {
			return fmt.Errorf("invalid duration (use 1h, 6h, 24h, 3d…)")
		}
		if d < time.Minute {
			return fmt.Errorf("interval must be at least 1m")
		}
		return m.panel.store.Update(func(c *Config) error {
			c.AutoBackupEvery = val
			return nil
		})
	case "auto_backup_mode":
		val = strings.ToLower(strings.TrimSpace(val))
		switch val {
		case protocol.ModeAuto, protocol.ModeFull, protocol.ModeIncremental:
		default:
			return fmt.Errorf("use auto, full, or incremental")
		}
		return m.panel.store.Update(func(c *Config) error {
			c.AutoBackupMode = val
			return nil
		})
	case "archive_offline_after":
		val = strings.TrimSpace(val)
		if _, err := ParseFlexibleDuration(val); err != nil {
			return fmt.Errorf("invalid duration (use 3d, 72h, or 0/empty)")
		}
		return m.panel.store.Update(func(c *Config) error {
			c.ArchiveOfflineAfter = val
			return nil
		})
	case "add_ssh_key":
		val = strings.TrimSpace(val)
		if val == "" {
			return fmt.Errorf("empty key")
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(val)); err != nil {
			return fmt.Errorf("invalid key: %w", err)
		}
		return m.panel.store.Update(func(c *Config) error {
			c.SSHAuthorizedKeys = append(c.SSHAuthorizedKeys, val)
			return nil
		})
	}
	return nil
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func scheduleTimeLabel(at string) string {
	if strings.TrimSpace(at) == "" {
		return "(any time)"
	}
	return at
}

func archiveAfterLabel(s string) string {
	if strings.TrimSpace(s) == "" || s == "0" {
		return "never"
	}
	return s
}

// partitionSummaries moves offline-too-long servers to the end and sets archivedFrom.
func (m *tuiModel) partitionSummaries() {
	online := m.panel.peers.OnlineIDs()
	after, err := ParseFlexibleDuration(m.panel.store.Get().ArchiveOfflineAfter)
	if err != nil {
		after = 0
	}
	var active, archived []ClientSummary
	now := time.Now()
	for _, s := range m.summaries {
		if _, on := online[s.Client.ID]; on {
			active = append(active, s)
			continue
		}
		if after > 0 && !s.Client.LastSeen.IsZero() && now.Sub(s.Client.LastSeen) >= after {
			archived = append(archived, s)
			continue
		}
		active = append(active, s)
	}
	m.summaries = append(active, archived...)
	m.archivedFrom = len(active)
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes", "enabled":
		return true, nil
	case "off", "false", "0", "no", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("use on or off")
	}
}

func (m *tuiModel) ensureVisible(n int) {
	m.ensureVisibleAt(m.cursor, n)
}

func (m *tuiModel) ensureVisibleAt(cur, n int) {
	listH := m.listHeight()
	if listH < 1 {
		listH = 1
	}
	if cur < m.offset {
		m.offset = cur
	}
	if cur >= m.offset+listH {
		m.offset = cur - listH + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	_ = n
}

func (m tuiModel) listHeight() int {
	return maxInt(1, m.height-6)
}

func (m tuiModel) View() string {
	w, h := m.width, m.height
	switch m.screen {
	case screenHelp:
		return m.viewHelp(w, h)
	case screenFind:
		box := styleInputBox.Width(minInt(w-4, 64)).Render(
			m.findTI.View() + "\nEsc cancel · Enter jump to client",
		)
		return placeOverlay(m.viewMain(w, h), box, w, h)
	case screenInput:
		rowLabel := m.inputKind
		for _, s := range m.settings {
			if s.Key == m.inputKind {
				rowLabel = s.Label
				break
			}
		}
		box := styleInputBox.Width(minInt(w-4, 72)).Render(
			styleDetailKey.Render(rowLabel) + "\n\n" + m.inputTI.View() + "\n\nEnter save · Esc cancel",
		)
		return placeOverlay(m.viewMain(w, h), box, w, h)
	case screenClient:
		return m.viewClient(w, h)
	default:
		return m.viewMain(w, h)
	}
}

// --- styles (same visual language as classic TUI managers) ---

var (
	styleTitle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("4")).
			Bold(true)

	styleKeybinds = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("7")).
			Bold(true)

	styleColHeader = lipgloss.NewStyle().Bold(true).Underline(true)

	styleCategory = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)

	styleActive = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)

	styleDim = lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Faint(true)

	styleSelected = lipgloss.NewStyle().Reverse(true)

	styleStatusOK = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))

	styleErr = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)

	styleDetailKey = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)

	styleFull = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)

	styleIncr = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))

	styleInputBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("4")).
			Padding(0, 1)
)

func (m tuiModel) viewTitle(w int) string {
	left := fmt.Sprintf("  backupurvm  [%s]", m.tabName())
	cli, bak, bytes, _ := m.panel.storage.DiskUsage()
	right := fmt.Sprintf("%d clients · %d backups · %s  ", cli, bak, common.FormatBytes(bytes))
	pad := w - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return styleTitle.Width(w).MaxWidth(w).Render(truncateW(left+strings.Repeat(" ", pad)+right, w))
}

func (m tuiModel) viewKeybinds(w int) string {
	var kb string
	switch {
	case m.screen == screenClient:
		kb = "  ↑/↓ backups   Esc back   D download   L latest full   B/I/Shift+B cmds   S settings   ? help"
	case m.tab == tabSettings:
		kb = "  ↑/↓ select   Enter edit   Esc back   U refresh   ? help   Q quit"
	default:
		kb = "  ↑/↓ select   Enter open   B auto  Shift+B full  I incr  P ping   Tab views   S settings   F find   ? help   Q quit"
	}
	if lipgloss.Width(kb) < w {
		kb += strings.Repeat(" ", w-lipgloss.Width(kb))
	}
	return styleKeybinds.Width(w).MaxWidth(w).Render(truncateW(kb, w))
}

func (m tuiModel) viewMain(w, h int) string {
	var b strings.Builder
	b.WriteString(m.viewTitle(w) + "\n")

	switch m.tab {
	case tabOverview:
		b.WriteString(m.bodyOverview(w, h))
	case tabClients:
		b.WriteString(m.bodyClients(w, h))
	case tabSettings:
		b.WriteString(m.bodySettings(w, h))
	}

	return fitHeight(b.String(), h)
}

func (m tuiModel) bodyOverview(w, h int) string {
	var b strings.Builder
	items := m.overviewItems()
	if m.overviewCursor >= len(items) && len(items) > 0 {
		m.overviewCursor = len(items) - 1
	}

	selKind := ""
	selIdx := -1
	if m.overviewCursor >= 0 && m.overviewCursor < len(items) {
		selKind = items[m.overviewCursor].kind
		selIdx = items[m.overviewCursor].idx
	}

	// --- Last Completed Task ---
	b.WriteString(styleCategory.Render("-- Last Completed Task ") + strings.Repeat("─", maxInt(0, w-24)) + "\n")
	if m.lastDone == nil {
		b.WriteString(styleDim.Render("  (none yet)") + "\n")
	} else {
		t := m.lastDone
		name := t.ClientName
		if name == "" {
			name = t.Hostname
		}
		when := relTime(t.FinishedAt)
		if t.FinishedAt.IsZero() {
			when = relTime(t.StartedAt)
		}
		if t.Status == TaskFailed {
			b.WriteString(styleErr.Render(fmt.Sprintf("  %-14s %-8s %-10s %-10s failed  %s",
				truncateW(name, 14), t.Mode, common.FormatBytes(t.BytesDone), when, truncateW(t.Message, maxInt(8, w-55)))) + "\n")
		} else {
			b.WriteString(styleFull.Render(fmt.Sprintf("  %-14s %-8s %-10s %-10s %s",
				truncateW(name, 14), t.Mode, common.FormatBytes(t.BytesDone), when, truncateW(t.SourceRoot, maxInt(8, w-50)))) + "\n")
		}
	}
	b.WriteByte('\n')

	// --- Currently Running Tasks ---
	b.WriteString(styleCategory.Render("-- Currently Running Tasks ") + strings.Repeat("─", maxInt(0, w-28)) + "\n")
	if len(m.running) == 0 {
		b.WriteString(styleDim.Render("  (idle — no transfers in progress)") + "\n")
	} else {
		b.WriteString(styleColHeader.Render(fmt.Sprintf("  %-14s %-8s %-18s %-10s %s", "CLIENT", "TYPE", "PROGRESS", "ELAPSED", "SOURCE")) + "\n")
		for i, t := range m.running {
			name := t.ClientName
			if name == "" {
				name = t.Hostname
			}
			row := fmt.Sprintf("  %-14s %-8s %-18s %-10s %s",
				truncateW(name, 14),
				t.Mode,
				progressLabel(t.BytesDone, t.BytesTotal),
				time.Since(t.StartedAt).Truncate(time.Second).String(),
				truncateW(t.SourceRoot, maxInt(8, w-58)),
			)
			row = truncateW(row, w)
			if selKind == "running" && selIdx == i {
				b.WriteString(styleSelected.Width(w).Render(row) + "\n")
			} else {
				b.WriteString(styleActive.Render(row) + "\n")
			}
		}
	}
	b.WriteByte('\n')

	// --- Servers ---
	online := m.panel.peers.OnlineIDs()
	b.WriteString(styleCategory.Render("-- Servers ") + strings.Repeat("─", maxInt(0, w-13)) + "\n")
	b.WriteString(styleColHeader.Render(fmt.Sprintf("  %-8s %-16s %-14s %-8s %-10s %-8s %s",
		"STATUS", "NAME", "HOST", "TYPE", "LAST", "ARCHIVES", "STORED")) + "\n")
	b.WriteString(strings.Repeat("─", w) + "\n")

	used := lipgloss.Height(b.String())
	listH := maxInt(1, h-used-2)
	serverStart := 0
	if selKind == "server" && selIdx >= listH {
		serverStart = selIdx - listH + 1
	}
	shown := 0
	seen := map[string]bool{}
	activeN := m.archivedFrom
	if activeN < 0 || activeN > len(m.summaries) {
		activeN = len(m.summaries)
	}
	if activeN == 0 && len(online) == 0 {
		b.WriteString(styleDim.Render("  (no servers — start: client --connect HOST:PORT --key KEY)") + "\n")
		shown++
	}
	archivedHeaderPrinted := false
	for i := serverStart; i < len(m.summaries) && shown < listH; i++ {
		if i == m.archivedFrom && !archivedHeaderPrinted {
			b.WriteString(styleCategory.Render("-- Archived ") + strings.Repeat("─", maxInt(0, w-14)) + "\n")
			archivedHeaderPrinted = true
			shown++
			if shown >= listH {
				break
			}
		}
		s := m.summaries[i]
		c := s.Client
		seen[c.ID] = true
		mode := s.LastMode
		if mode == "" {
			mode = "-"
		}
		status := "offline"
		if p, on := online[c.ID]; on {
			if p.Busy {
				status = "busy"
			} else {
				status = "online"
			}
		} else if i >= m.archivedFrom {
			status = "archive"
		}
		row := fmt.Sprintf("  %-8s %-16s %-14s %-8s %-10s %-8d %s",
			status,
			truncateW(c.Name, 16),
			truncateW(c.Hostname, 14),
			mode,
			relTime(c.LastSeen),
			s.BackupCount,
			common.FormatBytes(s.StoredBytes),
		)
		row = truncateW(row, w)
		if selKind == "server" && selIdx == i {
			b.WriteString(styleSelected.Width(w).Render(row) + "\n")
		} else if status == "online" {
			b.WriteString(styleActive.Render(row) + "\n")
		} else if status == "busy" {
			b.WriteString(styleIncr.Render(row) + "\n")
		} else {
			b.WriteString(styleDim.Render(row) + "\n")
		}
		shown++
	}
	for id, p := range online {
		if shown >= listH || seen[id] {
			continue
		}
		row := fmt.Sprintf("  %-8s %-16s %-14s %-8s %-10s %-8s %s",
			"online", truncateW(p.Name, 16), truncateW(p.Hostname, 14), "-", "-", "-", "-")
		row = truncateW(row, w)
		b.WriteString(styleActive.Render(row) + "\n")
		shown++
	}
	for shown < listH {
		b.WriteByte('\n')
		shown++
	}
	b.WriteString(strings.Repeat("─", w) + "\n")
	b.WriteString(m.statusLine(w) + "\n")
	b.WriteString(m.viewKeybinds(w))
	return b.String()
}

func progressLabel(done, total int64) string {
	if total <= 0 {
		return common.FormatBytes(done)
	}
	pct := int(done * 100 / total)
	return fmt.Sprintf("%s/%s %d%%", common.FormatBytes(done), common.FormatBytes(total), pct)
}

func (m tuiModel) bodyClients(w, h int) string {
	online := m.panel.peers.OnlineIDs()
	var b strings.Builder
	b.WriteString(styleColHeader.Render(
		fmt.Sprintf("  %-8s %-16s %-14s %-8s %-10s %-8s %-10s %s",
			"STATUS", "NAME", "HOST", "TYPE", "LAST", "ARCHIVES", "STORED", "FILES"),
	) + "\n")
	b.WriteString(strings.Repeat("─", w) + "\n")

	listH := m.listHeight()
	start := scrollStart(m.cursor, m.offset, listH)
	shown := 0

	if len(m.summaries) == 0 && len(online) == 0 {
		b.WriteString(styleCategory.Render("-- waiting for agents ") + strings.Repeat("─", maxInt(0, w-24)) + "\n")
		b.WriteString(styleDim.Render("  On each VPS run:") + "\n")
		b.WriteString(styleDim.Render("    client --connect HOST:PORT --key /path/to/key") + "\n")
		shown = 3
	}

	archivedHeaderPrinted := false
	for i := start; i < len(m.summaries) && shown < listH; i++ {
		if i == m.archivedFrom && m.archivedFrom < len(m.summaries) && !archivedHeaderPrinted {
			b.WriteString(styleCategory.Render("-- Archived ") + strings.Repeat("─", maxInt(0, w-14)) + "\n")
			archivedHeaderPrinted = true
			shown++
			if shown >= listH {
				break
			}
		}
		s := m.summaries[i]
		c := s.Client
		mode := s.LastMode
		if mode == "" {
			mode = "-"
		}
		status := "offline"
		if p, on := online[c.ID]; on {
			if p.Busy {
				status = "busy"
			} else {
				status = "online"
			}
		} else if i >= m.archivedFrom {
			status = "archive"
		}
		row := fmt.Sprintf("  %-8s %-16s %-14s %-8s %-10s %-8d %-10s %d",
			status,
			truncateW(c.Name, 16),
			truncateW(c.Hostname, 14),
			mode,
			relTime(c.LastSeen),
			s.BackupCount,
			common.FormatBytes(s.StoredBytes),
			len(c.Manifest),
		)
		row = truncateW(row, w)
		if i == m.cursor {
			b.WriteString(styleSelected.Width(w).Render(row) + "\n")
		} else if status == "online" {
			b.WriteString(styleActive.Render(row) + "\n")
		} else if status == "busy" {
			b.WriteString(styleIncr.Render(row) + "\n")
		} else {
			b.WriteString(styleDim.Render(row) + "\n")
		}
		shown++
	}
	for shown < listH {
		b.WriteByte('\n')
		shown++
	}
	b.WriteString(strings.Repeat("─", w) + "\n")
	b.WriteString(m.statusLine(w) + "\n")
	b.WriteString(m.viewKeybinds(w))
	return b.String()
}

func (m tuiModel) bodySettings(w, h int) string {
	var b strings.Builder
	b.WriteString(styleColHeader.Render(fmt.Sprintf("  %-28s  %s", "SETTING", "VALUE")) + "\n")
	b.WriteString(strings.Repeat("─", w) + "\n")

	listH := m.listHeight()
	// Build display rows with section headers
	type drow struct {
		kind string // hdr|row
		idx  int
		hdr  string
	}
	var rows []drow
	lastSec := ""
	for i, s := range m.settings {
		if s.Section != lastSec {
			rows = append(rows, drow{kind: "hdr", hdr: s.Section})
			lastSec = s.Section
		}
		rows = append(rows, drow{kind: "row", idx: i})
	}

	selDisp := 0
	for i, r := range rows {
		if r.kind == "row" && r.idx == m.cursor {
			selDisp = i
			break
		}
	}
	start := scrollStart(selDisp, m.offset, listH)
	shown := 0
	for i := start; i < len(rows) && shown < listH; i++ {
		r := rows[i]
		if r.kind == "hdr" {
			hdr := "-- " + r.hdr + " "
			b.WriteString(styleCategory.Render(hdr) + strings.Repeat("─", maxInt(0, w-lipgloss.Width(hdr))) + "\n")
			shown++
			continue
		}
		s := m.settings[r.idx]
		mark := " "
		if s.Editable {
			mark = "✎"
		}
		row := fmt.Sprintf("  %s %-26s  %s", mark, truncateW(s.Label, 26), truncateW(s.Value, maxInt(8, w-34)))
		row = truncateW(row, w)
		if r.idx == m.cursor {
			b.WriteString(styleSelected.Width(w).Render(row) + "\n")
		} else if s.Editable {
			b.WriteString(styleActive.Render(fmt.Sprintf("  %s %-26s", mark, truncateW(s.Label, 26))) + "  " + styleDim.Render(truncateW(s.Value, maxInt(8, w-34))) + "\n")
		} else {
			b.WriteString(styleDim.Render(row) + "\n")
		}
		shown++
	}
	for shown < listH {
		b.WriteByte('\n')
		shown++
	}
	b.WriteString(strings.Repeat("─", w) + "\n")
	b.WriteString(m.statusLine(w) + "\n")
	b.WriteString(m.viewKeybinds(w))
	return b.String()
}

func (m tuiModel) viewClient(w, h int) string {
	var b strings.Builder
	b.WriteString(m.viewTitle(w) + "\n")
	b.WriteString(styleColHeader.Render("  Backup history") + "\n")
	b.WriteString(strings.Repeat("─", w) + "\n")

	c := m.selected.Client
	b.WriteString(styleDetailKey.Render("  Client") + "  " + c.Name + "  (" + c.Hostname + ")\n")
	b.WriteString(styleDetailKey.Render("  ID    ") + "  " + c.ID + "\n")
	b.WriteString(styleDetailKey.Render("  Chain ") + fmt.Sprintf("  %d archives · %s on disk · %d files in latest manifest\n",
		m.selected.BackupCount, common.FormatBytes(m.selected.StoredBytes), len(c.Manifest)))
	if m.panel != nil && m.panel.downloads != nil {
		if active, url, _ := m.panel.downloads.Info(); active {
			b.WriteString(styleFull.Render("  DOWNLOAD  ") + truncateW(url, maxInt(10, w-12)) + "\n")
			b.WriteString(styleDim.Render("  Press D or L again to stop the download server.") + "\n")
		}
	}
	b.WriteByte('\n')
	b.WriteString(styleColHeader.Render(
		fmt.Sprintf("  %-20s %-10s %-10s %-6s %-6s %s", "BACKUP ID", "TYPE", "SIZE", "FILES", "DEL", "CREATED"),
	) + "\n")
	b.WriteString(strings.Repeat("─", w) + "\n")

	headerUsed := 10
	if m.panel != nil && m.panel.downloads != nil {
		if active, _, _ := m.panel.downloads.Info(); active {
			headerUsed = 12
		}
	}
	listH := maxInt(1, h-headerUsed-2)
	start := scrollStart(m.cursor, m.offset, listH)
	shown := 0
	if len(m.backups) == 0 {
		b.WriteString(styleDim.Render("  (no archives for this client)") + "\n")
		shown++
	}
	for i := start; i < len(m.backups) && shown < listH; i++ {
		r := m.backups[i]
		row := fmt.Sprintf("  %-20s %-10s %-10s %-6d %-6d %s",
			truncateW(r.ID, 20),
			r.Mode,
			common.FormatBytes(r.Bytes),
			r.FileCount,
			r.DeletedCount,
			r.CreatedAt.Local().Format("2006-01-02 15:04"),
		)
		row = truncateW(row, w)
		if i == m.cursor {
			b.WriteString(styleSelected.Width(w).Render(row) + "\n")
		} else if r.Mode == "full" {
			b.WriteString(styleFull.Render(row) + "\n")
		} else {
			b.WriteString(styleIncr.Render(row) + "\n")
		}
		shown++
	}
	for shown < listH {
		b.WriteByte('\n')
		shown++
	}
	b.WriteString(strings.Repeat("─", w) + "\n")
	b.WriteString(m.statusLine(w) + "\n")
	b.WriteString(m.viewKeybinds(w))
	return fitHeight(b.String(), h)
}

func (m tuiModel) viewHelp(w, h int) string {
	lines := []string{
		"",
		"  backupurvm host panel",
		"",
		"  Views",
		"    OVERVIEW   Last completed, running tasks, and servers",
		"    CLIENTS    Full server list with backup history  (Tab)",
		"    SETTINGS   All configuration  (press S)",
		"",
		"  Keys",
		"    ↑/↓  w/j     Move selection",
		"    Enter        Open client history / edit setting",
		"    Tab          Toggle OVERVIEW ↔ CLIENTS",
		"    B            Backup auto (selected online server)",
		"    Shift+B      Backup full",
		"    I            Backup incremental",
		"    P            Ping agent",
		"    D            Download selected archive (toggle HTTP server)",
		"    L            Download latest full (full + all incrementals merged)",
		"    S            Open Settings",
		"    Esc          Leave Settings (or back from history)",
		"    F            Find a client by name, host, or id",
		"    U            Refresh",
		"    Q            Quit (or leave Settings)",
		"    ?            Help",
		"",
		"  Download",
		"    D  Serve the selected archive as-is",
		"    L  Rebuild latest state: last full + incremental changes → one archive",
		"    URL: http://IPv4:PORT/UUID/FILENAME  (IP from ifconfig.com, IPv4 only)",
		"    Press D or L again to stop the download server.",
		"",
		"  Settings extras",
		"    Schedule time     Local HH:MM for auto backups (empty = any time)",
		"    Archive offline   Move servers offline longer than this to Archived",
		"",
		"  Press any key to return.",
	}
	var b strings.Builder
	b.WriteString(m.viewTitle(w) + "\n")
	for i := 0; i < h-2; i++ {
		if i < len(lines) {
			b.WriteString(truncateW(lines[i], w))
		}
		b.WriteByte('\n')
	}
	b.WriteString(m.viewKeybinds(w))
	return fitHeight(b.String(), h)
}

func (m tuiModel) statusLine(w int) string {
	if m.errMsg != "" {
		return styleErr.Render(truncateW(" ERROR: "+m.errMsg, w))
	}
	if m.panel != nil && m.panel.downloads != nil {
		if m.panel.downloads.Building() {
			return styleIncr.Render(truncateW(" Building latest full archive…", w))
		}
		if active, url, _ := m.panel.downloads.Info(); active && url != "" {
			return styleFull.Render(truncateW(" DOWNLOAD  "+url+"  (D/L to stop)", w))
		}
	}
	return styleStatusOK.Render(truncateW(" "+m.status, w))
}

func scrollStart(cur, offset, listH int) int {
	start := offset
	if cur < start {
		start = cur
	}
	if cur >= start+listH {
		start = cur - listH + 1
	}
	if start < 0 {
		start = 0
	}
	return start
}

func placeOverlay(base, overlay string, w, h int) string {
	ow := lipgloss.Width(overlay)
	oh := lipgloss.Height(overlay)
	x := maxInt(0, (w-ow)/2)
	y := maxInt(1, (h-oh)/2)
	baseLines := strings.Split(fitHeight(base, h), "\n")
	overLines := strings.Split(overlay, "\n")
	for len(baseLines) < h {
		baseLines = append(baseLines, "")
	}
	for i := 0; i < oh && y+i < len(baseLines); i++ {
		line := baseLines[y+i]
		for lipgloss.Width(line) < w {
			line += " "
		}
		baseLines[y+i] = truncateW(strings.Repeat(" ", x)+overLines[i], w)
	}
	return strings.Join(baseLines, "\n")
}

func fitHeight(s string, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func truncateW(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > w {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func relTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (p *SSHPanel) runTUI(s ssh.Session) {
	ptyReq, winCh, isPty := s.Pty()
	if !isPty {
		_, _ = io.WriteString(s, "backupurvm: interactive SSH (PTY) required\r\n")
		return
	}
	w, h := ptyReq.Window.Width, ptyReq.Window.Height
	if w < 1 {
		w = 80
	}
	if h < 1 {
		h = 24
	}

	// SSH writers aren't local TTYs. Under systemd/screen the host process also
	// has no TTY, so lipgloss package styles (bound at init) detect Ascii.
	// Do NOT SetDefaultRenderer — that orphans those styles. Force a color
	// profile on the existing default renderer they already reference.
	term := ptyReq.Term
	if term == "" {
		term = "xterm-256color"
	}
	environ := append(s.Environ(), "TERM="+term)
	if !hasEnvKey(environ, "COLORTERM") {
		environ = append(environ, "COLORTERM=truecolor")
	}
	sshEnv := sshEnviron(environ)
	out := termenv.NewOutput(s,
		termenv.WithEnvironment(sshEnv),
		termenv.WithUnsafe(),
		termenv.WithProfile(termenv.ANSI256),
	)
	// Mutate the same renderer package styles were created with.
	lipgloss.SetColorProfile(termenv.ANSI256)

	m := newTUI(p, s, w, h)
	prog := tea.NewProgram(m,
		tea.WithInput(s),
		tea.WithOutput(out),
		tea.WithAltScreen(),
		tea.WithEnvironment(environ),
	)

	go func() {
		for win := range winCh {
			prog.Send(tea.WindowSizeMsg{Width: win.Width, Height: win.Height})
		}
	}()

	if _, err := prog.Run(); err != nil {
		p.log.Printf("tui: %v", err)
	}
}

// sshEnviron adapts a []string env list to termenv.Environ.
type sshEnviron []string

func (e sshEnviron) Environ() []string { return e }

func (e sshEnviron) Getenv(key string) string {
	prefix := key + "="
	for _, kv := range e {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

func hasEnvKey(environ []string, key string) bool {
	prefix := key + "="
	for _, e := range environ {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
