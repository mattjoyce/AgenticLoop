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
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	apiBase := fs.String("api", "http://127.0.0.1:8090", "base URL for AgenticLoop API")
	token := fs.String("token", os.Getenv("AGENTICLOOP_API_TOKEN"), "Bearer token for API auth")
	pollInterval := fs.Duration("poll-interval", 2*time.Second, "poll interval while waiting for a run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: agenticloop watch [--api <url>] [--token <token>] [--poll-interval <duration>] [run_id]")
	}
	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("token is required (use --token or AGENTICLOOP_API_TOKEN)")
	}
	if *pollInterval <= 0 {
		return fmt.Errorf("poll-interval must be positive")
	}

	cfg := watchConfig{
		APIBase:      strings.TrimRight(*apiBase, "/"),
		Token:        *token,
		RunID:        fs.Arg(0),
		PollInterval: *pollInterval,
	}

	p := tea.NewProgram(newWatchModel(cfg), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type watchConfig struct {
	APIBase      string
	Token        string
	RunID        string
	PollInterval time.Duration
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

type workspaceSnapshotMsg struct {
	Summary workspaceSummary
	Err     string
}

type tokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u *tokenUsage) add(other tokenUsage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
}

type toolTokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	Calls            int `json:"calls"`
}

func (u *toolTokenUsage) add(other toolTokenUsage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.Calls += other.Calls
}

type stepMetrics struct {
	Tokens         tokenUsage
	ToolTokenUsage map[string]toolTokenUsage
}

type parsedStepOutput struct {
	Content        string
	TokenUsage     tokenUsage
	ToolTokenUsage map[string]toolTokenUsage
}

type workspaceFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

type workspaceSummary struct {
	RunID          string          `json:"run_id"`
	FileCount      int             `json:"file_count"`
	TotalSizeBytes int64           `json:"total_size_bytes"`
	Files          []workspaceFile `json:"files"`
}

type watchModel struct {
	cfg             watchConfig
	explicitRunID   bool
	waitingForRun   bool
	streamEvents    chan streamEventMsg
	width           int
	height          int
	connected       bool
	done            bool
	err             error
	runStatus       string
	events          []string
	stepMetrics     map[string]stepMetrics
	tokenTotals     tokenUsage
	toolTokenTotals map[string]toolTokenUsage
	workspace       workspaceSummary
	workspaceErr    string
}

func newWatchModel(cfg watchConfig) watchModel {
	waiting := cfg.RunID == ""
	status := "connecting"
	if waiting {
		status = "waiting"
	}
	return watchModel{
		cfg:             cfg,
		explicitRunID:   !waiting,
		waitingForRun:   waiting,
		streamEvents:    make(chan streamEventMsg, 32),
		runStatus:       status,
		stepMetrics:     map[string]stepMetrics{},
		toolTokenTotals: map[string]toolTokenUsage{},
	}
}

func (m watchModel) Init() tea.Cmd {
	if m.waitingForRun {
		return pollForRunCmd(m.cfg.APIBase, m.cfg.Token, m.cfg.PollInterval)
	}
	return tea.Batch(
		startEventStreamCmd(m.cfg, m.streamEvents),
		waitForStreamEventCmd(m.streamEvents),
		fetchWorkspaceCmd(m.cfg.APIBase, m.cfg.Token, m.cfg.RunID),
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
		return m, pollForRunCmd(m.cfg.APIBase, m.cfg.Token, m.cfg.PollInterval)
	case runFoundMsg:
		m.cfg.RunID = msg.RunID
		m.waitingForRun = false
		m.runStatus = "connecting"
		m.streamEvents = make(chan streamEventMsg, 32)
		m.stepMetrics = map[string]stepMetrics{}
		m.tokenTotals = tokenUsage{}
		m.toolTokenTotals = map[string]toolTokenUsage{}
		m.workspace = workspaceSummary{}
		m.workspaceErr = ""
		m.appendEvent(fmt.Sprintf("[%s] found run %s", time.Now().Format("15:04:05"), msg.RunID))
		return m, tea.Batch(
			startEventStreamCmd(m.cfg, m.streamEvents),
			waitForStreamEventCmd(m.streamEvents),
			fetchWorkspaceCmd(m.cfg.APIBase, m.cfg.Token, m.cfg.RunID),
		)
	case streamStartedMsg:
		m.connected = true
		if strings.TrimSpace(m.cfg.RunID) == "" {
			return m, nil
		}
		return m, fetchWorkspaceCmd(m.cfg.APIBase, m.cfg.Token, m.cfg.RunID)
	case workspaceSnapshotMsg:
		if msg.Err != "" {
			m.workspaceErr = msg.Err
			return m, nil
		}
		m.workspace = msg.Summary
		m.workspaceErr = ""
		return m, nil
	case streamEventMsg:
		if msg.Err != nil {
			m.err = msg.Err
			m.appendEvent("stream error: " + msg.Err.Error())
			return m, nil
		}
		if msg.EOF {
			m.appendEvent("stream closed by server")
			return m, m.resetToWaiting()
		}
		m.handleEvent(msg.Event, msg.Data)
		if m.done {
			return m, m.resetToWaiting()
		}
		return m, tea.Batch(
			waitForStreamEventCmd(m.streamEvents),
			fetchWorkspaceCmd(m.cfg.APIBase, m.cfg.Token, m.cfg.RunID),
		)
	default:
		return m, nil
	}
}

func (m watchModel) View() string {
	accent := lipgloss.Color("#F97316")
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#1C1007")).
		Background(accent).
		Padding(0, 1).
		Render("AgenticLoop Watch")

	statusStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#1C1007")).
		Background(accent).
		Padding(0, 1)
	switch m.runStatus {
	case "waiting":
		statusStyle = statusStyle.Background(lipgloss.Color("#6B7280"))
	case "done":
		statusStyle = statusStyle.Background(lipgloss.Color("#FDBA74"))
	case "failed":
		statusStyle = statusStyle.Background(lipgloss.Color("#EF4444")).Foreground(lipgloss.Color("#FFF7ED"))
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
		Foreground(lipgloss.Color("#FDBA74")).
		Render(fmt.Sprintf("run=%s  api=%s  stream=%s", runLabel, m.cfg.APIBase, streamLabel))

	status := statusStyle.Render(strings.ToUpper(m.runStatus))
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FDBA74")).
		Render("q: quit")
	if m.done {
		footer = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FDBA74")).
			Render("run finished, q: quit")
	}
	if m.err != nil {
		footer = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444")).
			Render("error: " + m.err.Error() + "  q: quit")
	}

	panelWidth := bodyWidth(m.width)
	eventsHeight, tokensHeight, workspaceHeight := panelHeights(m.height)

	eventLines := m.events
	if len(eventLines) == 0 {
		if m.waitingForRun {
			eventLines = []string{"waiting for active run..."}
		} else {
			eventLines = []string{"waiting for events..."}
		}
	}
	if len(eventLines) > eventsHeight-1 {
		eventLines = eventLines[len(eventLines)-(eventsHeight-1):]
	}
	eventsPanel := renderPanel("Events", eventLines, panelWidth, eventsHeight, accent, false)
	tokenPanel := renderPanel("Token Usage", m.tokenPanelLines(tokensHeight-1), panelWidth, tokensHeight, accent, true)
	workspacePanel := renderPanel("Workspace", m.workspacePanelLines(workspaceHeight-1), panelWidth, workspaceHeight, accent, true)

	return strings.Join([]string{title + " " + status, meta, eventsPanel, tokenPanel, workspacePanel, footer}, "\n")
}

func panelHeights(terminalHeight int) (events, tokens, workspace int) {
	available := terminalHeight - 5
	if available < 15 {
		available = 15
	}
	tokens = 6
	workspace = 7
	events = available - tokens - workspace
	if events < 6 {
		events = 6
		remaining := available - events
		tokens = remaining / 2
		workspace = remaining - tokens
		if tokens < 4 {
			tokens = 4
		}
		if workspace < 4 {
			workspace = 4
		}
	}
	return events, tokens, workspace
}

func renderPanel(title string, lines []string, width, height int, accent lipgloss.Color, keepHead bool) string {
	if height < 3 {
		height = 3
	}
	contentHeight := height - 1
	if contentHeight < 1 {
		contentHeight = 1
	}
	if len(lines) > contentHeight {
		if keepHead {
			lines = lines[:contentHeight]
		} else {
			lines = lines[len(lines)-contentHeight:]
		}
	}
	for len(lines) < contentHeight {
		lines = append(lines, "")
	}
	content := lipgloss.NewStyle().Bold(true).Foreground(accent).Render(title) + "\n" + strings.Join(lines, "\n")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Foreground(lipgloss.Color("#FFF7ED")).
		Background(lipgloss.Color("#2A1305")).
		Width(width).
		Height(height).
		Padding(0, 1).
		Render(content)
}

func (m *watchModel) handleEvent(event string, data []byte) {
	switch event {
	case "snapshot":
		var payload struct {
			Run struct {
				Status string `json:"status"`
			} `json:"run"`
			Steps []struct {
				ID         string          `json:"id"`
				StepNum    int             `json:"step_num"`
				Phase      string          `json:"phase"`
				Status     string          `json:"status"`
				ToolOutput json.RawMessage `json:"tool_output"`
			} `json:"steps"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			m.appendEvent("snapshot (unparsed)")
			return
		}
		m.runStatus = payload.Run.Status
		m.stepMetrics = map[string]stepMetrics{}
		for _, step := range payload.Steps {
			parsed := parseStepOutput(step.ToolOutput)
			m.stepMetrics[step.ID] = stepMetrics{
				Tokens:         parsed.TokenUsage,
				ToolTokenUsage: parsed.ToolTokenUsage,
			}
		}
		m.recalculateTokenTotals()
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
				ID         string          `json:"id"`
				StepNum    int             `json:"step_num"`
				Phase      string          `json:"phase"`
				Status     string          `json:"status"`
				Error      *string         `json:"error"`
				ToolOutput json.RawMessage `json:"tool_output"`
			} `json:"step"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			m.appendEvent(event + " (unparsed)")
			return
		}
		parsed := parseStepOutput(payload.Step.ToolOutput)
		line := fmt.Sprintf("[%s] %s #%d %s status=%s",
			time.Now().Format("15:04:05"),
			event,
			payload.Step.StepNum,
			payload.Step.Phase,
			payload.Step.Status,
		)
		if parsed.Content != "" {
			if tools, paths := parseToolOutput(parsed.Content); len(tools) > 0 {
				line += " tools=" + strings.Join(tools, ",")
				if len(paths) > 0 {
					line += " paths=" + strings.Join(paths, ",")
				}
			}
		}
		if parsed.TokenUsage.TotalTokens > 0 {
			line += fmt.Sprintf(" tok=%d", parsed.TokenUsage.TotalTokens)
		}
		if payload.Step.Error != nil && *payload.Step.Error != "" {
			line += " err=" + trimForLog(*payload.Step.Error, 60)
		}
		if payload.Step.ID != "" {
			m.stepMetrics[payload.Step.ID] = stepMetrics{
				Tokens:         parsed.TokenUsage,
				ToolTokenUsage: parsed.ToolTokenUsage,
			}
			m.recalculateTokenTotals()
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

func (m *watchModel) recalculateTokenTotals() {
	m.tokenTotals = tokenUsage{}
	m.toolTokenTotals = map[string]toolTokenUsage{}
	for _, metrics := range m.stepMetrics {
		m.tokenTotals.add(metrics.Tokens)
		for toolName, usage := range metrics.ToolTokenUsage {
			current := m.toolTokenTotals[toolName]
			current.add(usage)
			m.toolTokenTotals[toolName] = current
		}
	}
}

func (m *watchModel) tokenPanelLines(maxLines int) []string {
	lines := []string{
		fmt.Sprintf("job total: total=%d prompt=%d completion=%d", m.tokenTotals.TotalTokens, m.tokenTotals.PromptTokens, m.tokenTotals.CompletionTokens),
		"per-tool ACT usage (estimated split per tool-call round):",
	}
	if len(m.toolTokenTotals) == 0 {
		lines = append(lines, "  waiting for ACT token metadata...")
		return trimPanelLines(lines, maxLines)
	}
	type entry struct {
		name  string
		usage toolTokenUsage
	}
	entries := make([]entry, 0, len(m.toolTokenTotals))
	for name, usage := range m.toolTokenTotals {
		entries = append(entries, entry{name: name, usage: usage})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].usage.TotalTokens == entries[j].usage.TotalTokens {
			return entries[i].name < entries[j].name
		}
		return entries[i].usage.TotalTokens > entries[j].usage.TotalTokens
	})
	for _, e := range entries {
		lines = append(lines,
			fmt.Sprintf("  %s calls=%d total=%d (p=%d c=%d)",
				e.name,
				e.usage.Calls,
				e.usage.TotalTokens,
				e.usage.PromptTokens,
				e.usage.CompletionTokens,
			),
		)
	}
	return trimPanelLines(lines, maxLines)
}

func (m *watchModel) workspacePanelLines(maxLines int) []string {
	if m.workspaceErr != "" {
		return trimPanelLines([]string{"workspace unavailable: " + m.workspaceErr}, maxLines)
	}
	lines := []string{
		fmt.Sprintf("files=%d total=%s", m.workspace.FileCount, formatBytes(m.workspace.TotalSizeBytes)),
	}
	if len(m.workspace.Files) == 0 {
		lines = append(lines, "workspace not created yet")
		return trimPanelLines(lines, maxLines)
	}
	for _, f := range m.workspace.Files {
		lines = append(lines, fmt.Sprintf("  %s (%s)", f.Path, formatBytes(f.SizeBytes)))
	}
	return trimPanelLines(lines, maxLines)
}

func trimPanelLines(lines []string, maxLines int) []string {
	if maxLines <= 0 {
		return []string{}
	}
	if len(lines) <= maxLines {
		return lines
	}
	trimmed := append([]string{}, lines[:maxLines]...)
	trimmed[maxLines-1] = "..."
	return trimmed
}

func parseStepOutput(raw json.RawMessage) parsedStepOutput {
	if len(raw) == 0 {
		return parsedStepOutput{}
	}
	var payload struct {
		Content        string                    `json:"content"`
		TokenUsage     tokenUsage                `json:"token_usage"`
		ToolTokenUsage map[string]toolTokenUsage `json:"tool_token_usage"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return parsedStepOutput{}
	}
	out := parsedStepOutput{
		Content:    payload.Content,
		TokenUsage: payload.TokenUsage,
	}
	if len(payload.ToolTokenUsage) > 0 {
		out.ToolTokenUsage = payload.ToolTokenUsage
	}
	return out
}

func formatBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(size)
	for _, u := range units {
		v /= 1024.0
		if v < 1024.0 {
			return fmt.Sprintf("%.1f%s", v, u)
		}
	}
	return fmt.Sprintf("%.1fPB", v/1024.0)
}

func (m *watchModel) appendEvent(line string) {
	m.events = append(m.events, line)
	if len(m.events) > 800 {
		m.events = m.events[len(m.events)-800:]
	}
}

// resetToWaiting resets model state to poll for the next run.
// If a run_id was given explicitly on the CLI, it returns tea.Quit instead.
func (m *watchModel) resetToWaiting() tea.Cmd {
	if m.explicitRunID {
		return tea.Quit
	}
	m.cfg.RunID = ""
	m.waitingForRun = true
	m.connected = false
	m.done = false
	m.err = nil
	m.runStatus = "waiting"
	m.stepMetrics = map[string]stepMetrics{}
	m.tokenTotals = tokenUsage{}
	m.toolTokenTotals = map[string]toolTokenUsage{}
	m.workspace = workspaceSummary{}
	m.workspaceErr = ""
	return pollForRunCmd(m.cfg.APIBase, m.cfg.Token, m.cfg.PollInterval)
}

func pollForRunCmd(apiBase, token string, pollInterval time.Duration) tea.Cmd {
	return func() tea.Msg {
		if pollInterval <= 0 {
			pollInterval = 2 * time.Second
		}
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
			_ = json.NewDecoder(resp.Body).Decode(&runs)
			resp.Body.Close()
			if len(runs) > 0 {
				return runFoundMsg{RunID: runs[0].ID}
			}
		}
		time.Sleep(pollInterval)
		return pollTickMsg{}
	}
}

func fetchWorkspaceCmd(apiBase, token, runID string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(runID) == "" {
			return workspaceSnapshotMsg{}
		}
		u := fmt.Sprintf("%s/v1/runs/%s/workspace", apiBase, url.PathEscape(runID))
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return workspaceSnapshotMsg{Err: fmt.Sprintf("create workspace request: %v", err)}
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return workspaceSnapshotMsg{Err: fmt.Sprintf("workspace fetch: %v", err)}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			return workspaceSnapshotMsg{Err: fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))}
		}

		var payload workspaceSummary
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return workspaceSnapshotMsg{Err: fmt.Sprintf("decode workspace payload: %v", err)}
		}
		return workspaceSnapshotMsg{Summary: payload}
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
