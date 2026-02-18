package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	apiBase := fs.String("api", "http://127.0.0.1:8090", "base URL for AgenticLoop API")
	token := fs.String("token", os.Getenv("AGENTICLOOP_API_TOKEN"), "Bearer token for API auth")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: agenticloop watch [--api <url>] [--token <token>] <run_id>")
	}
	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("token is required (use --token or AGENTICLOOP_API_TOKEN)")
	}

	cfg := watchConfig{
		APIBase: strings.TrimRight(*apiBase, "/"),
		Token:   *token,
		RunID:   fs.Arg(0),
	}

	p := tea.NewProgram(newWatchModel(cfg), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type watchConfig struct {
	APIBase string
	Token   string
	RunID   string
}

type streamEventMsg struct {
	Event string
	Data  []byte
	Err   error
	EOF   bool
}

type streamStartedMsg struct{}

type watchModel struct {
	cfg          watchConfig
	streamEvents chan streamEventMsg
	width        int
	height       int
	connected    bool
	done         bool
	err          error
	runStatus    string
	events       []string
}

func newWatchModel(cfg watchConfig) watchModel {
	return watchModel{
		cfg:          cfg,
		streamEvents: make(chan streamEventMsg, 32),
		runStatus:    "connecting",
	}
}

func (m watchModel) Init() tea.Cmd {
	return tea.Batch(
		startEventStreamCmd(m.cfg, m.streamEvents),
		waitForStreamEventCmd(m.streamEvents),
	)
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	case streamStartedMsg:
		m.connected = true
		return m, nil
	case streamEventMsg:
		if msg.Err != nil {
			m.err = msg.Err
			m.appendEvent("stream error: " + msg.Err.Error())
			return m, nil
		}
		if msg.EOF {
			m.done = true
			m.appendEvent("stream closed by server")
			return m, nil
		}
		m.handleEvent(msg.Event, msg.Data)
		if m.done {
			return m, nil
		}
		return m, waitForStreamEventCmd(m.streamEvents)
	default:
		return m, nil
	}
}

func (m watchModel) View() string {
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#EAF3FF")).
		Background(lipgloss.Color("#1E3A8A")).
		Padding(0, 1).
		Render("AgenticLoop Watch")

	statusStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#0B1B36")).
		Background(lipgloss.Color("#FF9F43")).
		Padding(0, 1)
	if m.runStatus == "done" {
		statusStyle = statusStyle.Background(lipgloss.Color("#60A5FA"))
	}
	if m.runStatus == "failed" {
		statusStyle = statusStyle.Background(lipgloss.Color("#FB7185"))
	}

	meta := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A8C7FF")).
		Render(fmt.Sprintf("run=%s  api=%s  stream=%s", m.cfg.RunID, m.cfg.APIBase, connectionLabel(m.connected, m.done, m.err)))

	status := statusStyle.Render(strings.ToUpper(m.runStatus))
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFB86B")).
		Render("q: quit")
	if m.done {
		footer = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFB86B")).
			Render("run finished, q: quit")
	}
	if m.err != nil {
		footer = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FB7185")).
			Render("error: " + m.err.Error() + "  q: quit")
	}

	bodyHeight := m.height - 6
	if bodyHeight < 6 {
		bodyHeight = 6
	}
	lines := m.events
	if len(lines) == 0 {
		lines = []string{"waiting for events..."}
	}
	if len(lines) > bodyHeight {
		lines = lines[len(lines)-bodyHeight:]
	}

	body := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#1D4ED8")).
		Foreground(lipgloss.Color("#E5EEFF")).
		Background(lipgloss.Color("#0A1F44")).
		Width(bodyWidth(m.width)).
		Height(bodyHeight).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))

	return strings.Join([]string{title + " " + status, meta, body, footer}, "\n")
}

func (m *watchModel) handleEvent(event string, data []byte) {
	switch event {
	case "snapshot":
		var payload struct {
			Run struct {
				Status string `json:"status"`
			} `json:"run"`
			Steps []struct {
				StepNum int    `json:"step_num"`
				Phase   string `json:"phase"`
				Status  string `json:"status"`
			} `json:"steps"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			m.appendEvent("snapshot (unparsed)")
			return
		}
		m.runStatus = payload.Run.Status
		m.appendEvent(fmt.Sprintf("[%s] snapshot: %d step(s)", time.Now().Format("15:04:05"), len(payload.Steps)))
	case "run.updated":
		var payload struct {
			Run struct {
				Status  string  `json:"status"`
				Summary *string `json:"summary"`
				Error   *string `json:"error"`
			} `json:"run"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			m.appendEvent("run.updated (unparsed)")
			return
		}
		m.runStatus = payload.Run.Status
		line := fmt.Sprintf("[%s] run: %s", time.Now().Format("15:04:05"), payload.Run.Status)
		if payload.Run.Summary != nil && strings.TrimSpace(*payload.Run.Summary) != "" {
			line += " summary=" + trimForLog(*payload.Run.Summary, 80)
		}
		if payload.Run.Error != nil && strings.TrimSpace(*payload.Run.Error) != "" {
			line += " error=" + trimForLog(*payload.Run.Error, 80)
		}
		m.appendEvent(line)
	case "step.created", "step.updated":
		var payload struct {
			Step struct {
				StepNum int     `json:"step_num"`
				Phase   string  `json:"phase"`
				Status  string  `json:"status"`
				Tool    *string `json:"tool"`
				Error   *string `json:"error"`
			} `json:"step"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			m.appendEvent(event + " (unparsed)")
			return
		}
		tool := "-"
		if payload.Step.Tool != nil && *payload.Step.Tool != "" {
			tool = *payload.Step.Tool
		}
		line := fmt.Sprintf("[%s] %s #%d %s status=%s tool=%s",
			time.Now().Format("15:04:05"),
			event,
			payload.Step.StepNum,
			payload.Step.Phase,
			payload.Step.Status,
			tool,
		)
		if payload.Step.Error != nil && *payload.Step.Error != "" {
			line += " err=" + trimForLog(*payload.Step.Error, 60)
		}
		m.appendEvent(line)
	case "stream.closed":
		var payload struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			payload.Status = "unknown"
		}
		m.runStatus = payload.Status
		m.done = true
		m.appendEvent(fmt.Sprintf("[%s] stream closed status=%s", time.Now().Format("15:04:05"), payload.Status))
	case "error":
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			payload.Error = "unknown stream error"
		}
		m.err = fmt.Errorf(payload.Error)
		m.appendEvent(fmt.Sprintf("[%s] stream error: %s", time.Now().Format("15:04:05"), payload.Error))
	default:
		m.appendEvent(fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), event))
	}
}

func (m *watchModel) appendEvent(line string) {
	m.events = append(m.events, line)
	if len(m.events) > 800 {
		m.events = m.events[len(m.events)-800:]
	}
}

func startEventStreamCmd(cfg watchConfig, out chan streamEventMsg) tea.Cmd {
	return func() tea.Msg {
		go streamRunEvents(cfg, out)
		return streamStartedMsg{}
	}
}

func waitForStreamEventCmd(in <-chan streamEventMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-in
		if !ok {
			return streamEventMsg{EOF: true}
		}
		return msg
	}
}

func streamRunEvents(cfg watchConfig, out chan<- streamEventMsg) {
	defer close(out)

	u := fmt.Sprintf("%s/v1/runs/%s/events", cfg.APIBase, url.PathEscape(cfg.RunID))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		out <- streamEventMsg{Err: fmt.Errorf("create request: %w", err)}
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		out <- streamEventMsg{Err: fmt.Errorf("connect stream: %w", err)}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		out <- streamEventMsg{Err: fmt.Errorf("stream request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))}
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	var eventName string
	var dataLines []string

	flushEvent := func() {
		if len(dataLines) == 0 {
			eventName = ""
			return
		}
		if eventName == "" {
			eventName = "message"
		}
		out <- streamEventMsg{
			Event: eventName,
			Data:  []byte(strings.Join(dataLines, "\n")),
		}
		eventName = ""
		dataLines = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flushEvent()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			part := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(part, " ") {
				part = part[1:]
			}
			dataLines = append(dataLines, part)
		}
	}
	flushEvent()

	if err := scanner.Err(); err != nil {
		out <- streamEventMsg{Err: fmt.Errorf("read stream: %w", err)}
		return
	}
	out <- streamEventMsg{EOF: true}
}

func trimForLog(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func bodyWidth(terminalWidth int) int {
	if terminalWidth <= 0 {
		return 80
	}
	w := terminalWidth - 2
	if w < 40 {
		return 40
	}
	return w
}

func connectionLabel(connected, done bool, err error) string {
	if err != nil {
		return "error"
	}
	if done {
		return "closed"
	}
	if connected {
		return "open"
	}
	return "connecting"
}
