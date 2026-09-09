package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only this test executable handles these synthetic commands, never the user's CLI.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--fake-descendant" {
		for {
			_ = os.WriteFile(os.Args[2], []byte(time.Now().String()), 0600)
			time.Sleep(20 * time.Millisecond)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		if len(os.Args) > 2 && os.Args[2] == "--help" {
			fmt.Println("--model --sandbox --json --ephemeral --skip-git-repo-check --ignore-user-config")
			os.Exit(0)
		}
		fmt.Print(string(successOutput().Stdout))
		switch os.Getenv("SWITCH_CODEX_TEST_MODE") {
		case "fail":
			os.Exit(1)
		case "oversize":
			fmt.Print(strings.Repeat("x", MaxOutputBytes+1))
		case "wait":
			time.Sleep(10 * time.Second)
		case "tree":
			cmd := exec.Command(os.Args[0], "--fake-descendant", os.Getenv("SWITCH_CODEX_TEST_HEARTBEAT"))
			if err := cmd.Start(); err != nil {
				os.Exit(2)
			}
			time.Sleep(10 * time.Second)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestRealProcessRunner(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateCLI(context.Background(), exe, ProcessRunner{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode      string
		wantError bool
		timeout   time.Duration
	}{{"", false, 5 * time.Second}, {"fail", true, 5 * time.Second}, {"wait", true, 500 * time.Millisecond}, {"oversize", true, 5 * time.Second}} {
		t.Run(tc.mode, func(t *testing.T) {
			cmd := exec.Command(exe, "exec")
			cmd.Env = replaceEnv(os.Environ(), "SWITCH_CODEX_TEST_MODE", tc.mode)
			out, err := (ProcessRunner{}).Run(context.Background(), cmd, tc.timeout)
			if err != nil {
				t.Fatal(err)
			}
			if (completion(out) != nil) != tc.wantError {
				t.Fatalf("%+v", out)
			}
			if response(out) != "synthetic response" {
				t.Fatal("partial output lost")
			}
			if len(out.Stdout) > MaxOutputBytes {
				t.Fatal("unbounded output")
			}
		})
	}
}
func TestValidateCLIRequiresAllNonInteractiveFlags(t *testing.T) {
	flags := []string{"--model", "--sandbox", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config"}
	for missing := range flags {
		help := append([]string(nil), flags...)
		help = append(help[:missing], help[missing+1:]...)
		runner := runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) {
			return ProcessOutput{Success: true, Stdout: []byte(strings.Join(help, " "))}, nil
		})
		if err := ValidateCLI(context.Background(), "/synthetic/codex", runner); err == nil {
			t.Fatalf("accepted CLI help without %s", flags[missing])
		}
	}
}
func TestTimeoutAndCancellationKillDescendants(t *testing.T) {
	exe, _ := os.Executable()
	for _, cancelRun := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRun), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "heartbeat")
			cmd := exec.Command(exe, "exec")
			cmd.Env = replaceEnv(replaceEnv(os.Environ(), "SWITCH_CODEX_TEST_MODE", "tree"), "SWITCH_CODEX_TEST_HEARTBEAT", marker)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelRun {
				go func() { time.Sleep(time.Second); cancel() }()
			}
			out, err := (ProcessRunner{}).Run(ctx, cmd, 3*time.Second)
			if err != nil || out.Error == nil {
				t.Fatalf("%+v %v", out, err)
			}
			first, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal("descendant did not start: ", err)
			}
			time.Sleep(80 * time.Millisecond)
			second, _ := os.ReadFile(marker)
			if string(first) != string(second) {
				t.Fatal("descendant survived termination")
			}
		})
	}
}
func TestInvocationIsolationAndResponseContract(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "CODEX_THREAD_ID", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"} {
		t.Setenv(key, "synthetic")
	}
	cmd := invocation("/synthetic/codex", "/isolated/home", "/isolated/work", Prompt)
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, "CODEX_HOME=/isolated/home") {
		t.Fatal("missing isolated home")
	}
	for _, key := range []string{"OPENAI_API_KEY=", "CODEX_API_KEY=", "OPENAI_BASE_URL=", "CODEX_THREAD_ID=", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE="} {
		if strings.Contains(env, key) {
			t.Fatalf("leaked %s", key)
		}
	}
	for _, arg := range []string{Model, Prompt, "--ignore-user-config", "--ephemeral", "--json", "read-only", "features.shell_tool=false", "cli_auth_credentials_store=\"file\""} {
		found := false
		for _, actual := range cmd.Args {
			found = found || arg == actual
		}
		if !found {
			t.Fatalf("missing %s", arg)
		}
	}
	out := successOutput()
	out.Stdout = append(out.Stdout, out.Stdout...)
	if response(out) != "synthetic response" {
		t.Fatal("duplicate item")
	}
	out.Stdout = append(out.Stdout, []byte("{\"type\":\"turn.failed\"}\n")...)
	if completion(out) == nil {
		t.Fatal("accepted failed turn")
	}
}
