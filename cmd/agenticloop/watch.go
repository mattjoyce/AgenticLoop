package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
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
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: agenticloop watch [--api <url>] [--token <token>] [run_id]")
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

type runFoundMsg struct {
	RunID string
}

type pollTickMsg struct{}

type watchModel struct {
	cfg           watchConfig
	waitingForRun bool
	streamEvents  chan streamEventMsg
	width         int
	height        int
	connected     bool
	done          bool
	err           error
	runStatus     string
	events        []string
}

func newWatchModel(cfg watchConfig) watchModel {
	waiting := cfg.RunID == ""
	status := "connecting"
	if waiting {
		status = "waiting"
	}
	return watchModel{
		cfg:           cfg,
		waitingForRun: waiting,
		streamEvents:  make(chan streamEventMsg, 32),
		runStatus:     status,
	}
}

func (m watchModel) Init() tea.Cmd {
	if m.waitingForRun {
		return pollForRunCmd(m.cfg.APIBase, m.cfg.Token)
	}
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
	case pollTickMsg:
		return m, pollForRunCmd(m.cfg.APIBase, m.cfg.Token)
	case runFoundMsg:
		m.cfg.RunID = msg.RunID
		m.waitingForRun = false
		m.runStatus = "connecting"
		m.streamEvents = make(chan streamEventMsg, 32)
		m.appendEvent(fmt.Sprintf("[%s] found run %s", time.Now().Format("15:04:05"), msg.RunID))
		return m, tea.Batch(
			startEventStreamCmd(m.cfg, m.streamEvents),
			waitForStreamEventCmd(m.streamEvents),
		)
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
	switch m.runStatus {
	case "waiting":
		statusStyle = statusStyle.Background(lipgloss.Color("#6B7280"))
	case "done":
		statusStyle = statusStyle.Background(lipgloss.Color("#60A5FA"))
	case "failed":
		statusStyle = statusStyle.Background(lipgloss.Color("#FB7185"))
	}

	runLabel := m.cfg.RunID
	if runLabel == "" {
		runLabel = "-"
	}
	streamLabel := connectionLabel(m.connected, m.done, m.err)
	if m.waitingForRun {
		streamLabel = "polling"
	}
	meta := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A8C7FF")).
		Render(fmt.Sprintf("run=%s  api=%s  stream=%s", runLabel, m.cfg.APIBase, streamLabel))

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
		if m.waitingForRun {
			lines = []string{"waiting for active run..."}
		} else {
			lines = []string{"waiting for events..."}
		}
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
				StepNum    int     `json:"step_num"`
				Phase      string  `json:"phase"`
				Status     string  `json:"status"`
				Error      *string `json:"error"`
				ToolOutput *struct {
					Content string `json:"content"`
				} `json:"tool_output"`
			} `json:"step"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			m.appendEvent(event + " (unparsed)")
			return
		}
		line := fmt.Sprintf("[%s] %s #%d %s status=%s",
			time.Now().Format("15:04:05"),
			event,
			payload.Step.StepNum,
			payload.Step.Phase,
			payload.Step.Status,
		)
		if payload.Step.ToolOutput != nil && payload.Step.ToolOutput.Content != "" {
			if tools, paths := parseToolOutput(payload.Step.ToolOutput.Content); len(tools) > 0 {
				line += " tools=" + strings.Join(tools, ",")
				if len(paths) > 0 {
					line += " paths=" + strings.Join(paths, ",")
				}
			}
		}
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
		m.err = errors.New(payload.Error)
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

func pollForRunCmd(apiBase, token string) tea.Cmd {
	return func() tea.Msg {
		for _, status := range []string{"running", "queued"} {
			req, err := http.NewRequest(http.MethodGet, apiBase+"/v1/runs?status="+status, nil)
			if err != nil {
				break
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				break
			}
			var runs []struct {
				ID string `json:"id"`
			}
			json.NewDecoder(resp.Body).Decode(&runs)
			resp.Body.Close()
			if len(runs) > 0 {
				return runFoundMsg{RunID: runs[0].ID}
			}
		}
		time.Sleep(2 * time.Second)
		return pollTickMsg{}
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

var reToolName = regexp.MustCompile(`(?m)^Tool (\S+) output:`)

// parseToolOutput extracts tool names and any file paths from tool_output.content.
// Content format: "Tool <name> output:\n{json}\nTool <name> output:\n{json}\n..."
func parseToolOutput(content string) (tools []string, paths []string) {
	seen := map[string]bool{}
	for _, m := range reToolName.FindAllStringSubmatch(content, -1) {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			tools = append(tools, name)
		}
	}
	// Extract JSON blocks after each "Tool X output:" line and look for path/file fields.
	pathSeen := map[string]bool{}
	parts := reToolName.Split(content, -1)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// The JSON block is the first line(s) before any prose.
		lines := strings.SplitN(part, "\n", -1)
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "{") {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(l), &obj); err != nil {
				continue
			}
			for _, key := range []string{"path", "file", "filename", "dest"} {
				if v, ok := obj[key]; ok {
					if s, ok := v.(string); ok && s != "" && !pathSeen[s] {
						pathSeen[s] = true
						paths = append(paths, s)
					}
				}
			}
		}
	}
	return
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
