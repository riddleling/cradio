package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const version = "1.0.5"

type Station struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ===== List item =====

type stationItem struct{ s Station }

func (i stationItem) Title() string       { return i.s.Name }
func (i stationItem) Description() string { return "" }
func (i stationItem) FilterValue() string { return i.s.Name }

// ===== Styles =====

var (
	appTitleStyle = lipgloss.NewStyle().Bold(true)
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	helpStyle     = lipgloss.NewStyle().Faint(true)

	// 選中行的樣式（你可自行改顏色）
	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ee6ff8"))

	normalStyle = lipgloss.NewStyle()

	// 搜尋命中「額外效果」：不要指定顏色，讓 baseStyle（normal/selected）統一控制顏色
	filterMatchExtra = lipgloss.NewStyle().
				Underline(true)
)

// ===== Search highlight helper =====
// 重要：用 baseStyle 分段渲染，命中段用 base + extra，才能讓「整行選中顏色」也套用到命中段。
func renderWithHighlight(text, query string, base lipgloss.Style, matchExtra lipgloss.Style) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return base.Render(text)
	}

	lt := strings.ToLower(text)
	lq := strings.ToLower(q)

	matchStyle := base.Copy().Inherit(matchExtra)

	var b strings.Builder
	start := 0
	for {
		i := strings.Index(lt[start:], lq)
		if i < 0 {
			b.WriteString(base.Render(text[start:]))
			break
		}
		i += start

		b.WriteString(base.Render(text[start:i]))
		b.WriteString(matchStyle.Render(text[i : i+len(q)]))

		start = i + len(q)
	}
	return b.String()
}

// ===== Custom single-line delegate (no blank lines) =====

type singleLineDelegate struct{}

func (d singleLineDelegate) Height() int  { return 1 }
func (d singleLineDelegate) Spacing() int { return 0 }
func (d singleLineDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	return nil
}

func (d singleLineDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(stationItem)
	if !ok {
		return
	}

	rawName := i.Title()

	filterState := m.FilterState()
	filteringNow := (filterState == list.Filtering) // 正在輸入搜尋時

	// 搜尋輸入時不要顯示 ▶、不要選中顏色
	isSelected := (!filteringNow && index == m.Index())

	baseStyle := normalStyle
	prefix := "  "
	if isSelected {
		baseStyle = selectedStyle
		prefix = "▶ "
	}

	// 文字：搜尋時做命中高亮（同時保留整行選中顏色一致性）
	var name string
	if filterState == list.Filtering || filterState == list.FilterApplied {
		name = renderWithHighlight(rawName, m.FilterValue(), baseStyle, filterMatchExtra)
	} else {
		name = baseStyle.Render(rawName)
	}

	fmt.Fprint(w, baseStyle.Render(prefix)+name)
}

// ===== Bubble Tea model =====

type model struct {
	l list.Model

	nowPlaying string
	status     string

	p *player
}

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

	delegate := singleLineDelegate{}
	l := list.New(items, delegate, 0, 0)
	l.Title = "電台列表"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)

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
	title := appTitleStyle.Render(
		fmt.Sprintf("CRadio v%s - 線上廣播電台播放程式", version),
	)
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

func resolveMPVPath() (string, error) {
	mpvPath, err := exec.LookPath("mpv")
	if err != nil {
		return "", errors.New("找不到 mpv，請先安裝 mpv")
	}

	// Windows：優先使用 mpv.exe 而不是 mpv.com
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

func main() {
	cwd, _ := os.Getwd()
	p1 := filepath.Join(cwd, "list.json")

	stations, err := loadStationsFromJSON(p1)
	if err != nil {
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
