package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Station struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ===== List item =====

type stationItem struct{ s Station }

func (i stationItem) Title() string       { return i.s.Name }
func (i stationItem) Description() string { return "" }       // 不顯示 URL
func (i stationItem) FilterValue() string { return i.s.Name } // 搜尋只用名稱

// ===== Player =====

type player struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	mpvExe string // 解析後的 mpv 實際路徑（用於備援 kill）
}

func (p *player) IsPlaying() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil && p.cmd.Process != nil
}

func (p *player) Stop() error {
	if p == nil {
		return nil
	}

	// 先把需要的欄位在鎖內取出來，避免外部同時改動造成 nil deref
	p.mu.Lock()
	cmd := p.cmd
	cancel := p.cancel
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		// 確保狀態清乾淨
		p.mu.Lock()
		p.cmd = nil
		p.cancel = nil
		p.mu.Unlock()
		return nil
	}

	// 先 cancel context（對某些情況有幫助）
	if cancel != nil {
		cancel()
	}

	pid := cmd.Process.Pid

	// 多段式停止：Windows 特別需要強硬一點
	if runtime.GOOS == "windows" {
		// 1) taskkill 砍 PID + 子行程樹
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()

		// 2) 再保險：直接 Kill（避免 taskkill 失敗時）
		_ = cmd.Process.Kill()

		// 3) Wait 一小段時間，讓系統收掉
		done := make(chan struct{}, 1)
		go func() {
			_ = cmd.Wait()
			done <- struct{}{}
		}()

		select {
		case <-done:
			// ok
		case <-time.After(900 * time.Millisecond):
			// 4) 最後備援：如果還卡住，有些環境 mpv 會變成 mpv.exe/mpv.com 互相啟動
			//    這裡用 /IM mpv.exe 全砍（通常你一次只會跑一個 mpv）
			//    若你未來想同時多個播放，就把這段拿掉即可。
			_ = exec.Command("taskkill", "/IM", "mpv.exe", "/T", "/F").Run()
			_ = exec.Command("taskkill", "/IM", "mpv.com", "/T", "/F").Run()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	} else {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	// 最後在鎖內清空狀態
	p.mu.Lock()
	p.cmd = nil
	p.cancel = nil
	p.mu.Unlock()

	return nil
}

func resolveMPVPath() (string, error) {
	mpvPath, err := exec.LookPath("mpv")
	if err != nil {
		return "", errors.New("找不到 mpv，請先安裝：winget install mpv 或 winget install shinchiro.mpv")
	}

	// Windows：如果找到的是 mpv.com，優先嘗試同目錄的 mpv.exe（避免 wrapper/console stub 行為）
	if runtime.GOOS == "windows" {
		l := strings.ToLower(mpvPath)
		if strings.HasSuffix(l, "mpv.com") {
			tryExe := strings.TrimSuffix(mpvPath, "mpv.com") + "mpv.exe"
			if _, err := os.Stat(tryExe); err == nil {
				return tryExe, nil
			}
		}
	}

	return mpvPath, nil
}

func startMPV(url string) (*player, error) {
	mpvPath, err := resolveMPVPath()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	args := []string{
		"--no-video",
		"--no-ytdl", // 避免 ytdl_hook 噪音
		"--force-window=no",
		"--audio-display=no",
		"--cache=yes",
		"--cache-secs=15",
		"--really-quiet", // 降低輸出（再加上 stdout/stderr discard）
		url,
	}

	cmd := exec.CommandContext(ctx, mpvPath, args...)

	// 不要顯示 mpv 的輸出
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	return &player{
		cmd:    cmd,
		cancel: cancel,
		mpvExe: mpvPath,
	}, nil
}

// ===== Bubble Tea model =====

type model struct {
	l list.Model

	nowPlaying string
	status     string

	p *player
}

var (
	appTitleStyle   = lipgloss.NewStyle().Bold(true)
	statusStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	nowPlayingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	helpStyle       = lipgloss.NewStyle().Faint(true)
)

func loadStationsFromJSON(path string) ([]Station, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s []Station
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	out := make([]Station, 0, len(s))
	for _, st := range s {
		st.Name = strings.TrimSpace(st.Name)
		st.URL = strings.TrimSpace(st.URL)
		if st.Name == "" || st.URL == "" {
			continue
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, errors.New("list.json 沒有任何有效電台資料")
	}
	return out, nil
}

func initialModel(stations []Station) model {
	items := make([]list.Item, 0, len(stations))
	for _, st := range stations {
		items = append(items, stationItem{s: st})
	}

	delegate := list.NewDefaultDelegate()
	// 關鍵：不要顯示 Description，item 高度就會變成 1 行
	delegate.ShowDescription = false

	l := list.New(items, delegate, 0, 0)
	l.Title = "電台列表"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)

	return model{
		l:      l,
		status: "選擇電台並按 Enter 播放",
	}
}

func (m model) Init() tea.Cmd { return nil }

type playResultMsg struct {
	ok   bool
	name string
	err  error
	p    *player
}

func playStationCmd(st Station, old *player) tea.Cmd {
	return func() tea.Msg {
		// 先停掉舊的（切台/停止可靠關鍵）
		if old != nil {
			_ = old.Stop()
		}

		p, err := startMPV(st.URL)
		if err != nil {
			return playResultMsg{ok: false, name: st.Name, err: err}
		}
		return playResultMsg{ok: true, name: st.Name, p: p}
	}
}

type stoppedMsg struct{}

func stopCmd(p *player) tea.Cmd {
	return func() tea.Msg {
		if p != nil {
			_ = p.Stop()
		}
		return stoppedMsg{}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch t := msg.(type) {
	case tea.WindowSizeMsg:
		h := t.Height - 6
		if h < 8 {
			h = 8
		}
		m.l.SetSize(t.Width, h)
		return m, nil

	case tea.KeyMsg:
		switch t.String() {
		case "q", "Q", "ctrl+c":
			// 退出前一定停掉播放
			if m.p != nil && m.p.IsPlaying() {
				_ = m.p.Stop()
			}
			return m, tea.Quit

		case "enter":
			it, ok := m.l.SelectedItem().(stationItem)
			if !ok {
				return m, nil
			}
			m.status = fmt.Sprintf("正在切換/啟動：%s ...", it.s.Name)
			return m, playStationCmd(it.s, m.p)

		case "s", "S":
			if m.p != nil && m.p.IsPlaying() {
				m.status = "正在停止播放..."
				return m, stopCmd(m.p)
			}
		}

	case playResultMsg:
		if t.ok {
			m.p = t.p
			m.nowPlaying = t.name
			m.status = statusStyle.Render("播放中：") + " " + t.name
		} else {
			m.p = nil
			m.nowPlaying = ""
			m.status = errorStyle.Render("播放失敗：") + " " + t.err.Error()
		}
		return m, nil

	case stoppedMsg:
		m.p = nil
		m.nowPlaying = ""
		m.status = "已停止播放"
		return m, nil
	}

	m.l, cmd = m.l.Update(msg)
	return m, cmd
}

func (m model) View() string {
	title := appTitleStyle.Render("CRadio - 線上廣播電台播放程式")
	help := helpStyle.Render("↑↓ 選台 | Enter 播放/切台 | s 停止 | q 退出 | / 搜尋")

	return strings.Join([]string{
		title,
		"",
		m.l.View(),
		"",
		m.status,
		help,
	}, "\n")
}

func main() {
	// Load list.json from current working dir (or alongside the exe)
	cwd, _ := os.Getwd()
	p1 := filepath.Join(cwd, "list.json")

	stations, err := loadStationsFromJSON(p1)
	if err != nil {
		// Try next to executable
		if exe, e2 := os.Executable(); e2 == nil {
			p2 := filepath.Join(filepath.Dir(exe), "list.json")
			stations, err = loadStationsFromJSON(p2)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "讀取 list.json 失敗：", err)
		os.Exit(1)
	}

	m := initialModel(stations)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "程式錯誤：", err)
		os.Exit(1)
	}
}
