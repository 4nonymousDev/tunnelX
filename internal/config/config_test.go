package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadRealConfig 用用户实际手写的 config.json 格式验证解析。
// 现场症状是"尚未配置私钥路径"——即 KeyPath 加载后为空。此测试用同样的
// JSON 内容（含缩进不规整、字段顺序等原样特征）确认解析是否真的丢字段。
// TestLoadRealConfig validates parsing using the format of an actual hand-written config.json.
// The observed symptom was an empty KeyPath reported as unconfigured. This reproduces the
// original JSON's irregular indentation and field order to detect whether parsing drops fields.
func TestLoadRealConfig(t *testing.T) {
	raw := `{
  "id": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
  "name": "DESKTOP-EXAMPLE",

  "server_addr": "tunnel.example.com:2222",
  "server_user": "m1",
  "key_path": "tunnel_key",
    "tunnels": [
    {
      "kind": "export",
      "name": "nginx",
      "enabled": true,
      "local_host": "127.0.0.1",
      "local_port": 9999
    }
  ],
  "suppress_tray_hint": true
}`

	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if c.KeyPath != "tunnel_key" {
		t.Errorf("KeyPath = %q, 期望 %q", c.KeyPath, "tunnel_key")
	}
	if c.ServerAddr != "tunnel.example.com:2222" {
		t.Errorf("ServerAddr = %q", c.ServerAddr)
	}
	if len(c.Tunnels) != 1 {
		t.Fatalf("隧道数 = %d, 期望 1", len(c.Tunnels))
	}
	if c.Tunnels[0].LocalPort != 9999 {
		t.Errorf("LocalPort = %d, 期望 9999", c.Tunnels[0].LocalPort)
	}
}

func TestLoadPathIgnoresAndDropsLegacySSHUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"server_user":"root","key_path":"tunnel_key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "server_user") {
		t.Fatalf("legacy server_user was persisted: %s", data)
	}
}

func TestLoadPathAnchorsOwnedFilesToConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "client.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"client-a","name":"test","key_path":"keys/tunnel_key","tunnels":[{"kind":"export","local_port":8080}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tunnels) != 1 || cfg.Tunnels[0].ID == "" {
		t.Fatal("旧配置中的隧道未补齐稳定 ID")
	}
	key, err := cfg.ResolvedKeyPath()
	if err != nil {
		t.Fatal(err)
	}
	wantKey := filepath.Join(filepath.Dir(path), "keys", "tunnel_key")
	if key != wantKey {
		t.Fatalf("ResolvedKeyPath() = %q, want %q", key, wantKey)
	}
	knownHosts, err := cfg.KnownHostsPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(path), KnownHostsName); knownHosts != want {
		t.Fatalf("KnownHostsPath() = %q, want %q", knownHosts, want)
	}
	stateDir := filepath.Join(dir, "state")
	if err := cfg.SetStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	knownHosts, err = cfg.KnownHostsPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateDir, KnownHostsName); knownHosts != want {
		t.Fatalf("KnownHostsPath() with state dir = %q, want %q", knownHosts, want)
	}
}

func TestLoadPathRepairsDuplicateTunnelIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{"id":"client-a","tunnels":[
		{"id":"same","kind":"export","local_port":80},
		{"id":"same","kind":"export","local_host":"192.0.2.10","local_port":80},
		{"kind":"export","local_port":9999}
	]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tunnels[0].ID != "same" {
		t.Fatalf("首次出现的 ID 被意外修改: %q", cfg.Tunnels[0].ID)
	}
	seen := map[string]bool{}
	for _, tunnel := range cfg.Tunnels {
		if tunnel.ID == "" || seen[tunnel.ID] {
			t.Fatalf("隧道 ID 未补齐或仍重复: %+v", cfg.Tunnels)
		}
		seen[tunnel.ID] = true
	}
}

// TestLoadWithCommentKeys 验证带 _注释_ 前缀字段的示例配置也能解析。
// TestLoadWithCommentKeys verifies that example configurations with _comment_ prefixed fields also parse.
func TestLoadWithCommentKeys(t *testing.T) {
	raw := `{
  "_注释_": "说明文字",
  "id": "",
  "name": "",
  "server_addr": "1.2.3.4:2222",
  "server_user": "m1",
  "key_path": "tunnel_key",
  "tunnels": []
}`

	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if c.KeyPath != "tunnel_key" {
		t.Errorf("KeyPath = %q, 期望 tunnel_key", c.KeyPath)
	}
}

// TestResolvedKeyPath 验证相对私钥路径按 exe 目录解析，而非进程工作目录。
// 配置里通常写 "tunnel_key" 这样的相对路径。若按工作目录解析，双击运行时
// 恰好能用，但从命令行、计划任务、开机自启启动时就会报"文件不存在"——
// 且错误信息看不出是路径基准的问题。
// TestResolvedKeyPath verifies that relative private-key paths resolve from the executable
// directory rather than the process working directory. A value such as "tunnel_key" may
// work when double-clicked but fail from a shell, scheduled task, or startup entry with an
// unhelpful file-not-found error if resolved against the working directory.
func TestResolvedKeyPath(t *testing.T) {
	exeDir, err := Dir()
	if err != nil {
		t.Fatalf("取 exe 目录: %v", err)
	}

	t.Run("相对路径按 exe 目录解析", func(t *testing.T) {
		c := &Config{KeyPath: "tunnel_key"}
		got, err := c.ResolvedKeyPath()
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		want := filepath.Join(exeDir, "tunnel_key")
		if got != want {
			t.Errorf("解析得到 %q, 期望 %q", got, want)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("解析结果应为绝对路径, 得到 %q", got)
		}
	})

	t.Run("绝对路径原样保留", func(t *testing.T) {
		abs := filepath.Join(t.TempDir(), "my_key")
		c := &Config{KeyPath: abs}
		got, err := c.ResolvedKeyPath()
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if got != abs {
			t.Errorf("绝对路径被改写: %q -> %q", abs, got)
		}
	})

	t.Run("未配置时返回空", func(t *testing.T) {
		c := &Config{}
		got, err := c.ResolvedKeyPath()
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if got != "" {
			t.Errorf("未配置时应返回空串, 得到 %q", got)
		}
	})
}

// TestSavePreservesKeyPath 验证保存后重新加载不丢失 KeyPath。
// 若 Save 与 Load 的字段标签不一致，会表现为"改了配置重启就丢"。
// TestSavePreservesKeyPath verifies that KeyPath survives saving and reloading.
// Mismatched Save and Load tags would make configuration changes disappear after restart.
func TestSavePreservesKeyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	orig := &Config{
		ID:         "test-id",
		Name:       "测试机",
		ServerAddr: "1.2.3.4:2222",
		KeyPath:    "tunnel_key",
		Tunnels: []Tunnel{{
			Kind:      KindExport,
			Name:      "nginx",
			Enabled:   true,
			LocalHost: "127.0.0.1",
			LocalPort: 9999,
		}},
		path: path,
	}

	if err := orig.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}

	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("解析失败: %v\n内容:\n%s", err, data)
	}

	if back.KeyPath != orig.KeyPath {
		t.Errorf("往返后 KeyPath = %q, 期望 %q\n落盘内容:\n%s",
			back.KeyPath, orig.KeyPath, data)
	}
	if len(back.Tunnels) != 1 {
		t.Errorf("往返后隧道数 = %d, 期望 1\n落盘内容:\n%s", len(back.Tunnels), data)
	}
}

// TestNewConfigDefaultsKeyPath 验证全新配置带上默认私钥名，且与
// config.example.json 保持一致——两者不一致正是本次修复的缺陷。
// TestNewConfigDefaultsKeyPath verifies that a new configuration uses the default private-key
// name and matches config.example.json, whose previous mismatch caused the fixed defect.
func TestNewConfigDefaultsKeyPath(t *testing.T) {
	c := newDefault()
	if c.KeyPath != DefaultKeyName {
		t.Errorf("newDefault().KeyPath = %q, 期望 %q", c.KeyPath, DefaultKeyName)
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ex Config
	if err := json.Unmarshal(data, &ex); err != nil {
		t.Fatal(err)
	}
	if ex.KeyPath != DefaultKeyName {
		t.Errorf("config.example.json key_path = %q, DefaultKeyName = %q，两者应一致", ex.KeyPath, DefaultKeyName)
	}
}

// TestLoadKeepsExplicitEmptyKeyPath 验证已有配置里显式为空的 key_path 不被
// 悄悄改写。用户可能有意清空它，Load 不该替他做决定——补默认值只发生在
// 全新配置上。走的是 applyDefaults，与 Load() 实际调用的补默认值逻辑同一份代码。
// TestLoadKeepsExplicitEmptyKeyPath verifies that an explicitly empty key_path is not
// silently rewritten. Users may clear it intentionally; defaults belong only to new
// configurations. This exercises applyDefaults, the same defaulting path used by Load().
func TestLoadKeepsExplicitEmptyKeyPath(t *testing.T) {
	const raw = `{"id":"abc","name":"n","key_path":"","tunnels":[]}`
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	applyDefaults(&c)
	if c.KeyPath != "" {
		t.Errorf("KeyPath = %q, 已有配置的显式空值应保持不变", c.KeyPath)
	}
}

// TestLoadFromHitAndMiss 验证配置的"来源"与"是否命中磁盘"两个诊断信号。
// 用户报"配置没生效"时，第一件要确认的就是程序读了哪个文件、读到没有——
// 没有这两个信息只能靠猜。注意两个分支都必须填好 Path()：
// 没读到时若连预期路径都不报，诊断只解决了一半问题。
// TestLoadFromHitAndMiss verifies the configuration source and disk-hit diagnostics.
// When configuration appears ineffective, users need both the attempted path and whether
// it was found. Path() must be populated on both branches, including a miss.
func TestLoadFromHitAndMiss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	// 文件不存在：用默认配置，但仍要报告预期路径。
	// Missing file: use defaults but still report the expected path.
	miss, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom（文件不存在）: %v", err)
	}
	if miss.Loaded() {
		t.Error("文件不存在时 Loaded() 应为 false")
	}
	if miss.Path() != path {
		t.Errorf("Path() = %q, 期望 %q——没读到也要报告预期路径", miss.Path(), path)
	}

	// 文件存在：读入其内容，并标记为已命中。
	// Existing file: load its contents and mark the disk hit.
	const raw = `{"id":"abc","name":"n","server_addr":"h:2222","tunnels":[]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	hit, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom（文件存在）: %v", err)
	}
	if !hit.Loaded() {
		t.Error("文件存在时 Loaded() 应为 true")
	}
	if hit.Path() != path {
		t.Errorf("Path() = %q, 期望 %q", hit.Path(), path)
	}
	if hit.ServerAddr != "h:2222" {
		t.Errorf("ServerAddr = %q, 应读到文件内容", hit.ServerAddr)
	}
}

// TestSavePromptsBeforeClobber 验证「启动时没读到配置，但目标路径现在有文件」
// 这一情形下，Save 会先征询用户。
//
// 这正是一次真实数据丢失的路径：程序启动时目录里没有 config.json，用户随后
// 手写一份放进来，此时界面上任何一次保存（哪怕只是勾选「不再提示」）都会
// 用空默认配置静默覆盖它，且原子改名不留任何可恢复的残留。
// TestSavePromptsBeforeClobber verifies that Save asks before overwriting a target that
// appeared after startup did not find configuration. This reproduces a real data-loss path:
// a user writes config.json after startup, then any UI save silently replaces it with empty
// defaults, while atomic rename leaves no recoverable remnants.
func TestSavePromptsBeforeClobber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	const existing = `{"id":"用户手写的","name":"n","tunnels":[]}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	// 模拟"启动时没读到配置"：loaded 保持 false。
	// Simulate configuration missing at startup by keeping loaded false.
	c := newDefault()
	c.SetPathForTest(path)

	var asked int
	c.SetClobberPrompt(func(string) bool {
		asked++
		return false // 用户选择不覆盖 / The user declines to overwrite.
		// The user declines overwriting.
	})

	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if asked != 1 {
		t.Errorf("询问次数 = %d, 期望 1", asked)
	}

	// 用户拒绝覆盖，磁盘上必须还是原文件。
	// After refusal, the original file must remain on disk.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existing {
		t.Errorf("文件被覆盖了：%s", got)
	}
}

// TestSaveClobberConfirmed 验证用户同意后照常写入。
// TestSaveClobberConfirmed verifies that saving proceeds after user confirmation.
func TestSaveClobberConfirmed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	if err := os.WriteFile(path, []byte(`{"id":"旧的"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c := newDefault()
	c.ID = "新的"
	c.SetPathForTest(path)
	c.SetClobberPrompt(func(string) bool { return true })

	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := loadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ID != "新的" {
		t.Errorf("ID = %q, 用户同意后应已写入", reloaded.ID)
	}
}

// TestSaveNoPromptWhenLoaded 验证正常情形不打扰用户：
// 启动时读到了配置，后续保存就是在写自己刚读的那份文件，无需询问。
// TestSaveNoPromptWhenLoaded verifies that normal saves do not interrupt the user:
// when configuration was loaded at startup, later saves update that same known file.
func TestSaveNoPromptWhenLoaded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	if err := os.WriteFile(path, []byte(`{"id":"abc","tunnels":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := loadFrom(path) // loaded = true
	if err != nil {
		t.Fatal(err)
	}

	var asked int
	c.SetClobberPrompt(func(string) bool {
		asked++
		return true
	})

	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if asked != 0 {
		t.Errorf("询问次数 = %d, 正常保存不应打扰用户", asked)
	}
}

// TestSaveNoPromptWhenAbsent 验证目标不存在时不询问——没有东西会被覆盖。
// TestSaveNoPromptWhenAbsent verifies no prompt when the target is absent and nothing can be overwritten.
func TestSaveNoPromptWhenAbsent(t *testing.T) {
	dir := t.TempDir()

	c := newDefault()
	c.SetPathForTest(filepath.Join(dir, FileName))

	var asked int
	c.SetClobberPrompt(func(string) bool {
		asked++
		return true
	})

	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if asked != 0 {
		t.Errorf("询问次数 = %d, 目标不存在时不应询问", asked)
	}
	if !c.Loaded() {
		t.Error("由受保护的 Save 创建配置后应认领该文件")
	}
}

// TestSaveClobberWithoutPrompt 验证未注入回调时保持旧行为（直接覆盖）。
// manager 侧的保存不经 UI，没有可用的询问渠道；此时保存失败比覆盖更糟。
// TestSaveClobberWithoutPrompt verifies the legacy direct-overwrite behavior without a callback.
// Manager-side saves have no UI prompt channel, where failing to save is worse than overwriting.
func TestSaveClobberWithoutPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	if err := os.WriteFile(path, []byte(`{"id":"旧的"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c := newDefault()
	c.ID = "新的"
	c.SetPathForTest(path)
	// 不注入 SetClobberPrompt
	// Do not inject SetClobberPrompt.

	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := loadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ID != "新的" {
		t.Errorf("ID = %q, 无回调时应直接写入", reloaded.ID)
	}
}

// TestPromptlessSaveDoesNotDisarm 验证一次「没有回调可用」的保存不会永久
// 解除后续的覆盖保护。
//
// 反面情形：若 Save 无条件把 loaded 置为 true，那么 UI 注入回调之前的任何
// 一次保存都会让程序自认为"这份配置归我了"，用户之后手写放入的配置将被
// 静默覆盖——正是本保护要防的事故。
// TestPromptlessSaveDoesNotDisarm verifies that one save without an available callback
// does not permanently disable overwrite protection. If Save always marked loaded=true,
// any pre-UI save would claim ownership and later silently overwrite a user-written file.
func TestPromptlessSaveDoesNotDisarm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	c := newDefault()
	c.SetPathForTest(path)

	// 尚未注入回调时先保存一次。
	// Save once before a callback is injected.
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 用户随后手写一份配置放进来。
	// The user then places a hand-written configuration there.
	const handWritten = `{"id":"用户手写的"}`
	if err := os.WriteFile(path, []byte(handWritten), 0o600); err != nil {
		t.Fatal(err)
	}

	// 此时才注入回调——保护必须仍然生效。
	// Inject the callback now; protection must still be active.
	asked := 0
	c.SetClobberPrompt(func(string) bool { asked++; return false })
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if asked != 1 {
		t.Errorf("询问次数 = %d, 期望 1——保护被提前解除了", asked)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != handWritten {
		t.Errorf("用户手写配置被静默覆盖：%s", got)
	}
}

// TestSaveConfirmOnceThenQuiet 验证用户确认一次之后不再反复打扰。
// 每改一次设置、每加一条隧道都弹一次窗，比不保护还糟。
// TestSaveConfirmOnceThenQuiet verifies that one confirmation prevents repeated prompts.
// Prompting for every setting change and tunnel addition would be worse than no protection.
func TestSaveConfirmOnceThenQuiet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(`{"id":"旧的"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c := newDefault()
	c.SetPathForTest(path)

	asked := 0
	c.SetClobberPrompt(func(string) bool { asked++; return true })

	for i := 0; i < 3; i++ {
		if err := c.Save(); err != nil {
			t.Fatalf("第 %d 次 Save: %v", i+1, err)
		}
	}
	if asked != 1 {
		t.Errorf("询问次数 = %d, 确认一次后不应再问", asked)
	}
}
