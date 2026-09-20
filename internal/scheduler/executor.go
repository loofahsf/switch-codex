package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"switch-codex/internal/platform"
	"switch-codex/internal/store"
	"time"
)

// accountExecution keeps a successful CLI call distinct from a non-fatal
// credential write-back warning. The scheduled UI records the warning while
// still marking the task successful; manual warmups use the same behaviour.
type accountExecution struct {
	response string
	warning  error
	err      error
}

// executeAccount runs one credential-isolated Codex task. Callers own the
// runtime parent directory, which lets scheduled and manual work coexist
// without sharing temporary credentials or lifecycle state.
func executeAccount(ctx context.Context, st *store.Store, runtime, cli string, runner Runner, timeout time.Duration, a store.Snapshot, prompt string) accountExecution {
	original, err := a.Credentials()
	if err != nil {
		return accountExecution{err: err}
	}
	normalized, err := store.NormalizeAuth(original)
	if err != nil {
		return accountExecution{err: err}
	}
	dir := filepath.Join(runtime, uuid.NewString())
	defer os.RemoveAll(dir)
	home, work := filepath.Join(dir, "home"), filepath.Join(dir, "work")
	for _, d := range []string{dir, home, work} {
		if err = os.MkdirAll(d, 0700); err != nil {
			return accountExecution{err: errors.New("无法创建临时认证目录")}
		}
		if err = os.Chmod(d, 0700); err != nil {
			return accountExecution{err: errors.New("无法设置认证目录权限")}
		}
	}
	if err = platform.WriteAtomic(filepath.Join(home, "auth.json"), normalized, 0600); err != nil {
		return accountExecution{err: errors.New("无法准备临时认证文件")}
	}
	if ctx.Err() != nil {
		return accountExecution{err: errors.New("任务已中断")}
	}
	out, runErr := runner.Run(ctx, invocation(cli, home, work, prompt), timeout)
	// A refresh may succeed even when the model request fails or times out.
	var warning error
	refreshed, readErr := os.ReadFile(filepath.Join(home, "auth.json"))
	if readErr != nil {
		warning = errors.New("无法读取 CLI 刷新后的凭证")
	} else if !bytes.Equal(refreshed, normalized) {
		warning = st.PersistRefreshedAuth(a.ID, original, refreshed)
	}
	if runErr == nil {
		runErr = completion(out)
	}
	if runErr != nil && warning != nil {
		return accountExecution{response: response(out), err: fmt.Errorf("%w；%v", runErr, warning)}
	}
	return accountExecution{response: response(out), warning: warning, err: runErr}
}
