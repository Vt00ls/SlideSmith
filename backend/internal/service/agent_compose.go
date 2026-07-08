package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
)

type AgentComposeClient interface {
	Up(ctx context.Context, req AgentRunRequest) error
	Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error)
}

type AgentRunRequest struct {
	Phase        string
	Command      string
	Prompt       string
	WorkDir      string
	ComposeFile  string
	Detached     bool
	PollInterval time.Duration
}

type AgentRunResult struct {
	RunID         string
	SessionID     string
	Status        string
	ExitCode      *int
	WorkspacePath string
	RawJSON       string
	StderrTail    string
	ErrorMessage  string
}

type AgentComposeCLIClient struct {
	cfg config.AgentComposeConfig
}

func NewAgentComposeCLIClient(cfg config.AgentComposeConfig) *AgentComposeCLIClient {
	return &AgentComposeCLIClient{cfg: cfg}
}

func (c *AgentComposeCLIClient) Up(ctx context.Context, req AgentRunRequest) error {
	if !c.cfg.Enabled {
		return nil
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	args := c.baseArgs(req)
	args = append(args, "up", "--json")
	_, err := c.runCommand(ctx, args, req.WorkDir)
	return err
}

func (c *AgentComposeCLIClient) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	if !c.cfg.Enabled {
		return &AgentRunResult{Status: "disabled"}, nil
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	if req.Detached {
		return c.runDetached(ctx, req)
	}
	return c.runSync(ctx, req)
}

func (c *AgentComposeCLIClient) runSync(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	args := c.baseArgs(req)
	agent := c.cfg.Agent
	if agent == "" {
		agent = "ppt_master"
	}
	args = append(args, "run", agent)
	switch {
	case req.Prompt != "":
		args = append(args, "--prompt", req.Prompt)
	case req.Command != "":
		args = append(args, "--command", req.Command)
	default:
		return &AgentRunResult{Status: "failed"}, fmt.Errorf("agent run %s has neither command nor prompt", req.Phase)
	}
	args = append(args, "--json")
	output, err := c.runCommand(ctx, args, req.WorkDir)
	result := parseAgentRunResult(output)
	result.RawJSON = string(output)
	result.StderrTail = agentErrorStderrTail(err)
	if result.ExitCode == nil {
		result.ExitCode = agentErrorExitCode(err)
	}
	if result.WorkspacePath == "" && result.SessionID != "" && c.cfg.SessionDataRoot != "" {
		result.WorkspacePath = filepath.Join(c.cfg.SessionDataRoot, "sessions", result.SessionID, "workspace")
	}
	if err != nil {
		if result.Status == "" {
			result.Status = "failed"
		}
		if result.ErrorMessage == "" {
			result.ErrorMessage = agentErrorStderrTail(err)
		}
		return result, err
	}
	return result, nil
}

func (c *AgentComposeCLIClient) runDetached(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	args := c.baseArgs(req)
	agent := c.cfg.Agent
	if agent == "" {
		agent = "ppt_master"
	}
	args = append(args, "run", agent)
	switch {
	case req.Prompt != "":
		args = append(args, "--prompt", req.Prompt)
	case req.Command != "":
		args = append(args, "--command", req.Command)
	default:
		return &AgentRunResult{Status: "failed"}, fmt.Errorf("agent run %s has neither command nor prompt", req.Phase)
	}
	args = append(args, "--detach", "--json")
	output, err := c.runCommand(ctx, args, req.WorkDir)
	result := parseAgentRunResult(output)
	result.RawJSON = string(output)
	result.StderrTail = agentErrorStderrTail(err)
	if result.ExitCode == nil {
		result.ExitCode = agentErrorExitCode(err)
	}
	c.fillWorkspacePath(result)
	if err != nil {
		if result.Status == "" {
			result.Status = "failed"
		}
		if result.ErrorMessage == "" {
			result.ErrorMessage = agentErrorStderrTail(err)
		}
		return result, err
	}
	if result.RunID == "" {
		result.Status = "failed"
		return result, fmt.Errorf("agent run %s detached response did not include run_id", req.Phase)
	}

	interval := req.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		inspect, inspectErr := c.inspectRun(ctx, req, result.RunID)
		if inspect != nil {
			if inspect.RunID == "" {
				inspect.RunID = result.RunID
			}
			if inspect.SessionID == "" {
				inspect.SessionID = result.SessionID
			}
			c.fillWorkspacePath(inspect)
			result = inspect
		}
		if inspectErr != nil {
			if result.Status == "" {
				result.Status = "failed"
			}
			result.StderrTail = agentErrorStderrTail(inspectErr)
			if result.ErrorMessage == "" {
				result.ErrorMessage = result.StderrTail
			}
			return result, inspectErr
		}
		switch normalizeAgentRunStatus(result.Status) {
		case "succeeded":
			return result, nil
		case "failed", "canceled", "cancelled":
			if result.ErrorMessage == "" {
				result.ErrorMessage = fmt.Sprintf("agent run %s finished with status %s", req.Phase, result.Status)
			}
			return result, errors.New(result.ErrorMessage)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if result.Status == "" {
				result.Status = "running"
			}
			if result.ErrorMessage == "" {
				result.ErrorMessage = ctx.Err().Error()
			}
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *AgentComposeCLIClient) inspectRun(ctx context.Context, req AgentRunRequest, runID string) (*AgentRunResult, error) {
	args := c.baseArgs(req)
	args = append(args, "inspect", "run", runID, "--json")
	output, err := c.runCommand(ctx, args, req.WorkDir)
	result := parseAgentRunResult(output)
	result.RawJSON = string(output)
	result.StderrTail = agentErrorStderrTail(err)
	if result.ExitCode == nil {
		result.ExitCode = agentErrorExitCode(err)
	}
	if result.RunID == "" {
		result.RunID = runID
	}
	c.fillWorkspacePath(result)
	if err != nil {
		if result.Status == "" {
			result.Status = "failed"
		}
		if result.ErrorMessage == "" {
			result.ErrorMessage = agentErrorStderrTail(err)
		}
		return result, err
	}
	return result, nil
}

func (c *AgentComposeCLIClient) fillWorkspacePath(result *AgentRunResult) {
	if result == nil {
		return
	}
	if result.WorkspacePath == "" && result.SessionID != "" && c.cfg.SessionDataRoot != "" {
		result.WorkspacePath = filepath.Join(c.cfg.SessionDataRoot, "sessions", result.SessionID, "workspace")
	}
}

func (c *AgentComposeCLIClient) baseArgs(req AgentRunRequest) []string {
	args := []string{}
	if c.cfg.Host != "" {
		args = append(args, "--host", c.cfg.Host)
	}
	composeFile := req.ComposeFile
	if composeFile == "" {
		composeFile = c.cfg.ComposeFile
	}
	if composeFile != "" {
		args = append(args, "-f", composeFile)
	}
	return args
}

func (c *AgentComposeCLIClient) runCommand(ctx context.Context, args []string, workDir string) ([]byte, error) {
	cli := c.cfg.CLI
	if cli == "" {
		cli = "agent-compose"
	}
	cmd := exec.CommandContext(ctx, cli, args...)
	if workDir == "" {
		workDir = c.cfg.WorkDir
	}
	if workDir != "" {
		cmd.Dir = workDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitCode *int
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			exitCode = &code
		}
		return stdout.Bytes(), &AgentCommandError{
			CLI:      cli,
			Args:     append([]string(nil), args...),
			Err:      err,
			Stderr:   stderr.String(),
			ExitCode: exitCode,
		}
	}
	return stdout.Bytes(), nil
}

type AgentCommandError struct {
	CLI      string
	Args     []string
	Err      error
	Stderr   string
	ExitCode *int
}

func (e *AgentCommandError) Error() string {
	if strings.TrimSpace(e.Stderr) == "" {
		return fmt.Sprintf("%s %s: %v", e.CLI, strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("%s %s: %v: %s", e.CLI, strings.Join(e.Args, " "), e.Err, e.Stderr)
}

func (e *AgentCommandError) Unwrap() error {
	return e.Err
}

func agentErrorStderrTail(err error) string {
	if err == nil {
		return ""
	}
	var commandErr *AgentCommandError
	if ok := errors.As(err, &commandErr); ok && commandErr.Stderr != "" {
		return tailString(commandErr.Stderr, 4000)
	}
	return tailString(err.Error(), 4000)
}

func agentErrorExitCode(err error) *int {
	if err == nil {
		return nil
	}
	var commandErr *AgentCommandError
	if ok := errors.As(err, &commandErr); ok {
		return commandErr.ExitCode
	}
	return nil
}

func tailString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func (c *AgentComposeCLIClient) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return context.WithTimeout(ctx, timeout)
}

func parseAgentRunResult(raw []byte) *AgentRunResult {
	result := &AgentRunResult{}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return result
	}
	result.RunID = firstString(payload, "run_id", "runId", "id")
	result.SessionID = firstString(payload, "session_id", "sessionId")
	result.Status = firstString(payload, "status", "state")
	result.WorkspacePath = firstString(payload, "workspace_path", "workspacePath", "workspace")
	result.ErrorMessage = firstString(payload, "error", "error_message", "errorMessage", "message")
	if exitCode, ok := firstInt(payload, "exit_code", "exitCode"); ok {
		result.ExitCode = &exitCode
	}
	return result
}

func normalizeAgentRunStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func firstString(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if candidate, ok := typed[key].(string); ok && candidate != "" {
				return candidate
			}
		}
		for _, nested := range typed {
			if found := firstString(nested, keys...); found != "" {
				return found
			}
		}
	case []any:
		for _, nested := range typed {
			if found := firstString(nested, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}

func firstInt(value any, keys ...string) (int, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			switch candidate := typed[key].(type) {
			case float64:
				return int(candidate), true
			case int:
				return candidate, true
			}
		}
		for _, nested := range typed {
			if found, ok := firstInt(nested, keys...); ok {
				return found, true
			}
		}
	case []any:
		for _, nested := range typed {
			if found, ok := firstInt(nested, keys...); ok {
				return found, true
			}
		}
	}
	return 0, false
}
