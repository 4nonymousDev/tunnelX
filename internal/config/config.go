// Package config 读写 exe 同目录下的 config.json。
// Package config reads and writes config.json beside the executable.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	FileName       = "config.json"
	KnownHostsName = "known_hosts"

	// DefaultKeyName 是新建配置默认使用的私钥文件名。
	// 与 config.example.json、README、DEPLOY.md 保持一致——
	// 密钥与 exe 同目录、名字固定，整个目录拷到别的机器仍可用。
	// DefaultKeyName is the private-key file name for new configurations. It matches the
	// examples and documentation so copying the executable directory remains portable.
	DefaultKeyName = "tunnel_key"
	// SSHUser is an internal protocol value. The TunnelX server authenticates
	// public keys and does not expose an operating-system SSH account.
	SSHUser = "tunnelx"
)

// TunnelKind 区分两种隧道方向。
// TunnelKind distinguishes the two tunnel directions.
type TunnelKind string

const (
	// KindExport 把本地端口推到服务器（-R 远程转发）。
	// KindExport publishes a local port to the server using -R remote forwarding.
	KindExport TunnelKind = "export"
	// KindImport 把服务器端口拉到本地（-L 本地转发）。
	// KindImport exposes a server port locally using -L local forwarding.
	KindImport TunnelKind = "import"
)

// Tunnel 是一条隧道配置。
// Tunnel is one tunnel configuration.
type Tunnel struct {
	ID   string     `json:"id"`
	Kind TunnelKind `json:"kind"`
	Name string     `json:"name"` // 隧道名，可选；空则 UI 显示端口号 / Optional tunnel name; the UI falls back to the port.
	// Optional tunnel name; the UI falls back to the port.
	Enabled bool `json:"enabled"` // 用户意图：是否应处于运行状态 / User intent: whether this tunnel should be running.
	// User intent: whether the tunnel should be running.

	// Export 模式：把 LocalHost:LocalPort 推到服务端。
	// 服务端端口不在此配置——一律由服务端动态分配。
	// Export mode publishes LocalHost:LocalPort; the server port is allocated dynamically.
	LocalHost string `json:"local_host,omitempty"` // 默认 127.0.0.1 / Defaults to 127.0.0.1.
	// Defaults to 127.0.0.1.
	LocalPort int `json:"local_port,omitempty"`

	// Import 模式：在本地 ListenPort 监听，转发到 PeerID 那台机器的导出隧道。
	// 新配置按「对端身份 + 隧道 ID」匹配；旧配置没有 PeerTunnelID 时按源端口兼容。
	// 服务端端口是动态分配的，不进入持久化配置。
	// Import mode listens on ListenPort and forwards to an exported tunnel on PeerID.
	// New configs match peer identity plus stable tunnel ID; legacy configs fall back to
	// source port. The dynamic server port is never persisted.
	PeerID string `json:"peer_id,omitempty"` // 对端 Exporter UUID / Peer Exporter UUID.
	// Peer exporter UUID.
	PeerTunnelID string `json:"peer_tunnel_id,omitempty"` // 对端导出隧道的稳定 ID；旧配置为空时按端口兼容 / Stable peer tunnel ID; legacy empty values match by port.
	// Stable peer tunnel ID; legacy empty values fall back to the port.
	PeerName string `json:"peer_name,omitempty"` // 对端显示名，仅供 UI 回显 / Peer display name for UI rendering only.
	// Peer display name, used only by the UI.
	PeerSrcPort int `json:"peer_src_port,omitempty"` // 对端的源端口 / Peer's source port.
	// Peer's source port.
	ListenPort int `json:"listen_port,omitempty"` // 本地监听端口 / Local listen port.
	// Local listening port.
}

// Config 是完整的配置文件内容。
// Config is the complete configuration-file content.
type Config struct {
	// ID 是本机的稳定身份，首次启动生成后永不改变。
	// 不能只用计算机名做身份：Windows 默认名重名概率虽低，但用户手动改名
	// （DEV-PC、WIN10）或从同一镜像克隆的机器/虚拟机，重名很常见，会导致
	// 重连后匹配到错误的机器。
	// ID is the stable machine identity generated on first launch. Hostnames are unsafe
	// identifiers because renamed or cloned Windows machines commonly collide.
	ID string `json:"id"`

	// Name 是显示名，默认取计算机名，用户可改。仅用于 UI 显示与日志可读性，
	// 不参与匹配——因此用户改名不影响已建立的匹配关系。
	// Name is a user-editable display name, defaulting to the hostname. It is used only
	// for UI/log readability and never participates in identity matching.
	Name string `json:"name"`

	// 一个实例只连接一台服务器。
	// One instance connects to exactly one server.
	ServerAddr string `json:"server_addr"` // host:port，如 example.com:22 / host:port, for example example.com:22.
	// host:port, for example example.com:22.
	KeyPath string `json:"key_path"` // 私钥路径，相对路径以 exe 目录为基准；新建配置默认 DefaultKeyName / Private-key path; relative paths use the executable directory.
	// Private-key path; relative paths use the executable directory and new configs default to DefaultKeyName.

	Tunnels []Tunnel `json:"tunnels"`

	// SuppressTrayHint 记录用户是否勾选了"不再提示"。
	// Deprecated: 桌面 UI 会把该旧字段迁移到自己的偏好文件。
	// SuppressTrayHint records the legacy "do not show again" choice. Deprecated: the
	// desktop UI migrates it to its own preferences file.
	SuppressTrayHint bool `json:"suppress_tray_hint,omitempty"`

	path string // 加载来源，Save 时写回 / Load source written back by Save.
	// Load source and Save destination.
	stateDir string // known_hosts、日志和本地控制端点；空表示便携模式 / known_hosts, logs, and local control endpoint; empty means portable mode.
	// State directory; empty selects portable mode.

	// loaded 记录配置是否已"归本程序所有"：启动时从磁盘读到，
	// 或用户已确认覆盖。用于识别"启动时没有、运行期间才出现"的
	// 配置文件，避免 Save 静默覆盖用户手写的内容。
	// loaded records whether this program owns the configuration because it read it at
	// startup or the user approved replacement. It prevents Save from silently overwriting
	// a file created manually while the program was running.
	loaded bool

	// clobberPrompt 在「启动时没读到配置、目标路径却已有文件」时征询用户。
	// 用回调注入而非直接弹窗，是因为本包不依赖 UI，而 Save 也被 manager 调用——
	// 与 KeyPermPrompt、HostKeyPrompt 同一套路。
	// clobberPrompt asks before replacing a file that appeared after startup. A callback
	// keeps this package UI-independent and mirrors other injected prompts.
	clobberPrompt ClobberPrompt
}

// ClobberPrompt 在保存将覆盖一份「本程序未曾读取过」的配置文件时征询用户。
// 返回 true 表示用户同意覆盖。
//
// 存在的意义：程序启动时若目录下没有 config.json，内存里是空的默认配置；
// 用户随后手写一份放进来，此时界面上任何一次保存都会静默覆盖它，
// 而原子改名不留可恢复的残留。
// ClobberPrompt asks before overwriting a configuration this program never read;
// true permits replacement. This protects a file manually added after startup, which
// otherwise could be atomically overwritten with no recoverable residue.
type ClobberPrompt func(path string) (overwrite bool)

// SetClobberPrompt 注入覆盖确认回调。须在任何 Save 之前调用。
// SetClobberPrompt installs the overwrite-confirmation callback before any Save.
func (c *Config) SetClobberPrompt(p ClobberPrompt) { c.clobberPrompt = p }

// Dir 返回 exe 所在目录。配置、私钥、known_hosts 都放这里——工具设计为
// "拷过去双击就用"，配置必须跟着 exe 走。
// Dir returns the executable directory. Portable mode keeps configuration, private key,
// and known_hosts there so the entire directory can be copied and run.
func Dir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位程序路径: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe // 符号链接解析失败不致命，退回原始路径 / Symlink resolution failure is nonfatal; use the original path.
		// Symlink resolution is nonfatal; fall back to the original path.
	}
	return filepath.Dir(resolved), nil
}

// Load 读取配置。文件不存在时返回带默认值的新配置（未落盘）。
// Load reads configuration, returning an unsaved default when the file does not exist.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return LoadPath(filepath.Join(dir, FileName))
}

// LoadPath 从显式路径读取配置。相对路径会转换为绝对路径，因此私钥、
// known_hosts 与其他运行文件始终能稳定地以配置文件目录为基准解析，
// 不受启动时工作目录影响。
// LoadPath reads an explicit path and makes it absolute so keys, known_hosts, and other
// runtime files resolve consistently from the config directory, independent of cwd.
func LoadPath(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("配置文件路径不能为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件路径: %w", err)
	}
	return loadFrom(filepath.Clean(abs))
}

// loadFrom 从指定路径读取配置，是 Load 的可测接缝——
// Load 的路径由 Dir() 写死为 exe 目录，测试无法改写，
// 而"读到了/没读到"的分支正是需要覆盖的部分。
// loadFrom is Load's test seam: Load fixes its path to the executable directory, while
// tests need to exercise both file-found and file-missing branches.
func loadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		c := newDefault()
		c.path = path
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", FileName, err)
	}

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", FileName, err)
	}
	c.path = path
	c.loaded = true
	applyDefaults(&c)
	return &c, nil
}

// applyDefaults 补齐历史配置或人工编辑遗漏的必填项。
// 注意：只补 ID 与 Name。key_path 不在此列——用户可能有意清空它，
// 替他填回去属于擅自做主，且会把"未配置私钥"这个明确的错误
// 变成"文件不存在"这个更难懂的错误。补默认值只发生在全新配置上（见 newDefault）。
// applyDefaults fills required values omitted by legacy or manual configurations.
// The former server_user JSON property is ignored as an unknown legacy field. An empty
// key_path may be deliberate, so it remains untouched.
func applyDefaults(c *Config) {
	if c.ID == "" {
		c.ID = newID()
	}
	if c.Name == "" {
		c.Name = hostname()
	}
	EnsureUniqueTunnelIDs(c.Tunnels)
}

// EnsureTunnelID assigns the stable identity used by concurrent frontends.
func EnsureTunnelID(t *Tunnel) {
	if t != nil && t.ID == "" {
		t.ID = newID()
	}
}

// EnsureUniqueTunnelIDs 补齐空 ID，并修复人工复制配置造成的重复 ID。
// 首次出现的 ID 保持不变，后续重复项获得新 ID，避免 UI、API 与发布注册表
// 把两条不同隧道误认为同一条。
// EnsureUniqueTunnelIDs fills missing IDs and repairs duplicates from copied configs.
// The first occurrence stays unchanged; later duplicates get new IDs.
func EnsureUniqueTunnelIDs(tunnels []Tunnel) {
	seen := make(map[string]bool, len(tunnels))
	for i := range tunnels {
		id := tunnels[i].ID
		if id == "" || seen[id] {
			for {
				id = newID()
				if !seen[id] {
					break
				}
			}
			tunnels[i].ID = id
		}
		seen[id] = true
	}
}

// SetPathForTest 指定配置的落盘路径。
// 仅供测试使用：正常运行时路径由 Load 依据 exe 所在目录确定，
// 而测试需要写入临时目录以免污染真实配置。
// SetPathForTest sets the persistence path so tests can use a temporary directory.
func (c *Config) SetPathForTest(path string) { c.path = path }

// Path 返回配置文件的路径。即使文件尚不存在也会返回预期路径——
// 用户报"配置没生效"时，第一件事就是核对程序在看哪个文件。
// Path returns the expected configuration path even if the file is absent, aiding diagnosis.
func (c *Config) Path() string { return c.path }

// Loaded 报告配置是否确实从磁盘读到。false 表示用的是全新默认配置，
// 通常意味着该路径下还没有 config.json。
// Loaded reports whether configuration came from disk; false usually means a new default.
func (c *Config) Loaded() bool { return c.loaded }

// Save 原子写回配置：先写临时文件再改名，避免写入中途崩溃导致配置损坏。
// Save atomically writes through a temporary file and rename to avoid partial corruption.
func (c *Config) Save() error {
	if c.path == "" {
		dir, err := Dir()
		if err != nil {
			return err
		}
		c.path = filepath.Join(dir, FileName)
	}

	// 启动时没读到配置，目标却已存在——说明这份文件是程序运行期间出现的
	// （用户手写放入，或从别处拷来）。直接覆盖会毁掉它且无从恢复，先问一句。
	// 未注入回调时（如 manager 侧的保存）保持旧行为：此时没有询问渠道，
	// 保存失败比覆盖更糟。
	// If the target appeared after startup, ask before destroying an externally created
	// file. Without an injected prompt, preserve legacy behavior because there is no UI.
	claimAfterWrite := false
	if !c.loaded {
		if _, err := os.Stat(c.path); err == nil && c.clobberPrompt != nil {
			if !c.clobberPrompt(c.path) {
				return nil // 用户拒绝，不是错误 / User cancellation is not an error.
				// User cancellation is not an error.
			}
			// 用户已确认，这份配置自此归本程序所有，后续保存不必再问。
			// User confirmation establishes ownership, so later saves need not ask again.
			c.loaded = true
		} else if errors.Is(err, fs.ErrNotExist) && c.clobberPrompt != nil {
			// 已安装交互保护且目标确实不存在：若本次写入成功，文件就是
			// 本程序创建的，后续保存不应把它误判成外部文件。
			// With protection installed and no target, a successful write creates an owned file.
			claimAfterWrite = true
		}
		// 注意 loaded 只在"确实征询过用户"时置位。若此处无条件置位，
		// 一次没有回调可用的保存（如 UI 尚未注入时 manager 侧的保存）
		// 就会永久解除后续所有保护——用户之后手写的配置将被静默覆盖。
		// Set loaded only after an actual prompt. An unconditional assignment would let an
		// early promptless save permanently disable protection for later handwritten files.
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	data = append(data, '\n')

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入配置: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("保存配置: %w", err)
	}
	if claimAfterWrite {
		c.loaded = true
	}
	return nil
}

// KnownHostsPath 返回 known_hosts 的路径。
// KnownHostsPath returns the known_hosts path.
func (c *Config) KnownHostsPath() (string, error) {
	dir, err := c.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, KnownHostsName), nil
}

// SetStateDir 设置可写运行状态目录。空值恢复便携模式（配置文件目录）。
// SetStateDir sets the writable state directory; empty restores portable mode.
func (c *Config) SetStateDir(path string) error {
	if path == "" {
		c.stateDir = ""
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析状态目录: %w", err)
	}
	c.stateDir = filepath.Clean(abs)
	return nil
}

// StateDir 返回 known_hosts、日志和本地控制凭据所属的可写目录。
// StateDir returns the writable directory for known_hosts, logs, and local credentials.
func (c *Config) StateDir() (string, error) {
	if c.stateDir != "" {
		return c.stateDir, nil
	}
	return c.BaseDir()
}

// BaseDir 返回该配置拥有的文件目录。显式加载的配置以配置文件所在目录
// 为准；仅为兼容旧的便携模式，无路径配置才退回可执行文件目录。
// BaseDir returns the directory owned by this config. Explicit configs use their own
// directory; only legacy pathless portable configs fall back to the executable directory.
func (c *Config) BaseDir() (string, error) {
	if c.path != "" {
		return filepath.Dir(c.path), nil
	}
	return Dir()
}

// ResolvedKeyPath 返回私钥的绝对路径。
// 配置中通常写相对路径（如 "tunnel_key"），一律以配置文件目录为基准。
// 便携模式的配置就在可执行文件旁，因此保持了原有"整个目录拷走即可"的行为；
// 显式 --config 则不再受启动工作目录影响。
// ResolvedKeyPath returns the absolute private-key path. Relative paths resolve from the
// config directory, preserving portable mode while making --config independent of cwd.
func (c *Config) ResolvedKeyPath() (string, error) {
	if c.KeyPath == "" {
		return "", nil
	}
	if filepath.IsAbs(c.KeyPath) {
		return c.KeyPath, nil
	}
	dir, err := c.BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, c.KeyPath), nil
}

func newDefault() *Config {
	return &Config{
		ID:      newID(),
		Name:    hostname(),
		KeyPath: DefaultKeyName,
		Tunnels: []Tunnel{},
	}
}

// newID 生成 128 位随机标识。用十六进制而非 UUID 格式，省一个依赖；
// 用途只要求唯一且稳定，格式无所谓。
// newID generates a random 128-bit identifier. Hex avoids a UUID dependency; only stable uniqueness matters.
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败意味着系统熵源不可用，属于不应继续运行的环境问题。
		// A crypto/rand failure means the system entropy source is unavailable; continuing is unsafe.
		panic(fmt.Sprintf("生成本机标识失败: %v", err))
	}
	return hex.EncodeToString(b)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown-host"
	}
	return h
}
