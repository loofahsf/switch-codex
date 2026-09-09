package scheduler

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

func DetectCLIPath() *string {
	path, err := ResolveCLI(nil)
	if err != nil {
		return nil
	}
	return &path
}
func ResolveCLI(configured *string) (string, error) {
	if configured != nil {
		if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(*configured), ".exe") {
			return "", errors.New("Windows 请指定 codex.exe，不支持 .cmd 或 .bat 脚本")
		}
		if filepath.IsAbs(*configured) && isFile(*configured) {
			return *configured, nil
		}
		return "", errors.New("CLI 路径必须是存在的可执行文件绝对路径")
	}
	dirs := filepath.SplitList(os.Getenv("PATH"))
	if home, err := os.UserHomeDir(); err == nil {
		for _, suffix := range []string{".local/bin", ".cargo/bin", ".npm-global/bin"} {
			dirs = append(dirs, filepath.Join(home, suffix))
		}
		versions, _ := filepath.Glob(filepath.Join(home, ".nvm/versions/node/*/bin"))
		sort.Sort(sort.Reverse(sort.StringSlice(versions)))
		dirs = append(dirs, versions...)
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	if path := findCLI(dirs); path != "" {
		return path, nil
	}
	return "", errors.New("未找到 Codex CLI，请安装 CLI 或填写可执行文件绝对路径")
}
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
func findCLI(dirs []string) string {
	name := "codex"
	if runtime.GOOS == "windows" {
		name = "codex.exe"
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if isFile(candidate) {
			absolute, _ := filepath.Abs(candidate)
			return absolute
		}
		if runtime.GOOS == "windows" {
			for _, pkg := range []string{"codex/node_modules/@openai/codex-win32-x64", "codex"} {
				candidate = filepath.Join(dir, "node_modules/@openai", pkg, "vendor/x86_64-pc-windows-msvc/codex/codex.exe")
				if isFile(candidate) {
					absolute, _ := filepath.Abs(candidate)
					return absolute
				}
			}
		}
	}
	return ""
}
func cliCommand(path string, args ...string) *exec.Cmd {
	cmd := exec.Command(path, args...)
	cmd.Env = replaceEnv(os.Environ(), "PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cmd
}
func replaceEnv(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, v := range env {
		if !strings.EqualFold(strings.SplitN(v, "=", 2)[0], key) {
			result = append(result, v)
		}
	}
	return append(result, key+"="+value)
}
func invocation(cli, home, work, prompt string) *exec.Cmd {
	cmd := cliCommand(cli, "exec", "--model", Model, "--sandbox", "read-only", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--color", "never", "-c", "approval_policy=\"never\"", "-c", "cli_auth_credentials_store=\"file\"", "-c", "model_reasoning_effort=\"low\"", "-c", "web_search=\"disabled\"", "-c", "features.shell_tool=false", prompt)
	cmd.Dir = work
	cmd.Env = replaceEnv(cmd.Env, "CODEX_HOME", home)
	filtered := cmd.Env[:0]
	for _, v := range cmd.Env {
		key := strings.ToUpper(strings.SplitN(v, "=", 2)[0])
		switch key {
		case "CODEX_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_THREAD_ID", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE":
			continue
		}
		filtered = append(filtered, v)
	}
	cmd.Env = filtered
	return cmd
}
func ValidateCLI(ctx context.Context, path string, runner Runner) error {
	out, err := runner.Run(ctx, cliCommand(path, "exec", "--help"), 10*time.Second)
	if err != nil {
		return err
	}
	if out.Error != nil {
		return out.Error
	}
	if out.Success {
		valid := true
		for _, flag := range []string{"--model", "--sandbox", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config"} {
			valid = valid && strings.Contains(string(out.Stdout), flag)
		}
		if valid {
			return nil
		}
	}
	return errors.New("Codex CLI 版本不兼容，请升级至支持非交互任务所需参数的版本")
}
