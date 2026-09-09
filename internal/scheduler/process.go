package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type ProcessOutput struct {
	Success bool
	Stdout  []byte
	Error   error
}
type Runner interface {
	Run(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error)
}
type ProcessRunner struct{}
type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	overflow chan struct{}
	once     sync.Once
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(p), MaxOutputBytes-len(b.data))
	b.data = append(b.data, p[:n]...)
	if n < len(p) {
		b.once.Do(func() { close(b.overflow) })
	}
	return len(p), nil
}
func (ProcessRunner) Run(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (ProcessOutput, error) {
	if ctx.Err() != nil {
		return ProcessOutput{}, errors.New("任务已中断")
	}
	out := &boundedOutput{overflow: make(chan struct{})}
	cmd.Stdout = out
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.WaitDelay = time.Second
	prepareProcess(cmd)
	if err := cmd.Start(); err != nil {
		return ProcessOutput{}, errors.New("无法启动 Codex CLI，请检查路径和执行权限")
	}
	kill, cleanup, err := attachProcess(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ProcessOutput{}, err
	}
	defer cleanup()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitErr, runErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		runErr = errors.New("任务已中断")
		kill()
		waitErr = <-done
	case <-timer.C:
		runErr = errors.New("Codex CLI 调用超时")
		kill()
		waitErr = <-done
	case <-out.overflow:
		runErr = errors.New("CLI 输出超出限制")
		kill()
		waitErr = <-done
	}
	kill() // Also reap descendants that outlived a successful parent.
	select {
	case <-out.overflow:
		if runErr == nil {
			runErr = errors.New("CLI 输出超出限制")
		}
	default:
	}
	if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) && cmd.ProcessState == nil && runErr == nil {
		runErr = errors.New("无法获取 CLI 执行状态")
	}
	out.mu.Lock()
	data := bytes.Clone(out.data)
	out.mu.Unlock()
	return ProcessOutput{Success: cmd.ProcessState != nil && cmd.ProcessState.Success(), Stdout: data, Error: runErr}, nil
}
func completion(out ProcessOutput) error {
	if out.Error != nil {
		return out.Error
	}
	completed, failed := false, false
	for _, line := range bytes.Split(out.Stdout, []byte{'\n'}) {
		var e struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &e) == nil {
			if e.Type == "turn.completed" {
				completed = true
			}
			if e.Type == "turn.failed" {
				failed = true
			}
		}
	}
	if out.Success && completed && !failed {
		return nil
	}
	return errors.New("调用失败：未收到成功完成事件，请检查账号认证、模型权限、额度或网络")
}
func response(out ProcessOutput) string {
	messages := []string{}
	ids := map[string]bool{}
	for _, line := range bytes.Split(out.Stdout, []byte{'\n'}) {
		var e struct {
			Type string `json:"type"`
			Item *struct {
				ID   *string `json:"id"`
				Type string  `json:"type"`
				Text *string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "item.completed" || e.Item == nil || e.Item.Type != "agent_message" || e.Item.Text == nil {
			continue
		}
		if e.Item.ID != nil {
			if ids[*e.Item.ID] {
				continue
			}
			ids[*e.Item.ID] = true
		}
		messages = append(messages, *e.Item.Text)
	}
	return strings.Join(messages, "\n\n")
}
