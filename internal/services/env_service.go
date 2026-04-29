package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"go-python-runner/internal/runner"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// EnvInfo describes the venv the running app is using. Surfaced unchanged
// to the frontend; ToolName drives which CLI EnvService shells out to,
// Editable controls whether install/uninstall buttons are shown.
type EnvInfo struct {
	PythonPath    string `json:"pythonPath"`
	PythonVersion string `json:"pythonVersion"`
	VenvPath      string `json:"venvPath"`
	ToolName      string `json:"toolName"` // "uv" or "pip"
	Editable      bool   `json:"editable"`
}

// Package is one row of `pip list --format=json`.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// EnvService is a Wails service that inspects and mutates the venv the
// running app resolved. Install/uninstall serialize through a mutex to
// prevent pip lock-file races; ListPackages is lock-free.
type EnvService struct {
	info     EnvInfo
	mu       sync.Mutex
	busy     atomic.Bool
	app      atomic.Pointer[application.App]
	logger   *slog.Logger
	commandHook commandHook // injectable for tests; nil → real exec
}

// commandHook lets tests inject a fake exec.CommandContext. Production code
// uses the default (nil) which falls back to exec.CommandContext.
type commandHook func(ctx context.Context, name string, args ...string) *exec.Cmd

// NewEnvService probes the resolved venv and the available tooling. Returns
// an error only if there's no venv at all; an unwritable venv yields a
// service with Editable=false (install/uninstall still callable, but they
// fail-fast with a clear error).
func NewEnvService(logger *slog.Logger) (*EnvService, error) {
	venv, err := runner.FindVenv()
	if err != nil {
		return nil, fmt.Errorf("locating venv: %w", err)
	}

	toolName := "pip"
	if _, lookErr := exec.LookPath("uv"); lookErr == nil {
		toolName = "uv"
	}

	info := EnvInfo{
		PythonPath: venv.Python,
		VenvPath:   venv.Root,
		ToolName:   toolName,
		Editable:   venv.Editable,
	}
	// Best-effort version probe. Failures don't block service creation —
	// the version is informational, not load-bearing.
	{
		probe := exec.Command(venv.Python, "--version")
		runner.HideConsole(probe)
		if out, err := probe.CombinedOutput(); err == nil {
			info.PythonVersion = strings.TrimSpace(string(out))
		}
	}

	logger.Info("env service initialized",
		"python", info.PythonPath,
		"venv", info.VenvPath,
		"tool", info.ToolName,
		"version", info.PythonVersion,
		"editable", info.Editable,
		"source", "backend",
	)

	return &EnvService{info: info, logger: logger}, nil
}

// SetApp wires the Wails app reference for emitting env:operation:* events.
// Same lifecycle as RunnerService/LogService/ScriptService SetApp.
func (s *EnvService) SetApp(app *application.App) {
	s.app.Store(app)
}

// GetEnvInfo returns the resolved venv summary. Used by the EnvironmentPane
// header and by any caller that needs to know which Python is active.
func (s *EnvService) GetEnvInfo() EnvInfo {
	return s.info
}

// EnvBusy reports whether an install/uninstall is currently in flight. The
// frontend uses this for an immediate guard on the UI, but the source of
// truth for "are we busy" is the env:operation:start / env:operation:end
// event pair.
func (s *EnvService) EnvBusy() bool {
	return s.busy.Load()
}

// ListPackages parses `pip list --format=json` (or `uv pip list` equivalent)
// into typed records. No lock; pip list is read-only.
func (s *EnvService) ListPackages() ([]Package, error) {
	args := s.cmdArgs("list", "--format=json")
	out, err := s.runCapture(args)
	if err != nil {
		s.logger.Error("ListPackages failed", "error", err.Error(), "source", "backend")
		return nil, fmt.Errorf("listing packages: %w", err)
	}
	var pkgs []Package
	if err := json.Unmarshal(out, &pkgs); err != nil {
		s.logger.Error("ListPackages parse failed", "error", err.Error(), "source", "backend")
		return nil, fmt.Errorf("parsing pip list output: %w", err)
	}
	return pkgs, nil
}

// InstallPackage installs a single package spec (e.g. "pandas",
// "numpy>=2.0", "git+https://..."). Streamed pip output is forwarded as
// env:operation:log events; the final result is captured by env:operation:end.
func (s *EnvService) InstallPackage(spec, indexURL string) error {
	if spec == "" {
		err := errors.New("install spec cannot be empty")
		s.logger.Error("InstallPackage rejected", "error", err.Error(), "source", "backend")
		return err
	}
	if !s.info.Editable {
		err := fmt.Errorf("venv at %s is not writable", s.info.VenvPath)
		s.logger.Error("InstallPackage rejected", "error", err.Error(), "source", "backend")
		return err
	}
	return s.runInstall("install", spec, indexURL, []string{spec})
}

// InstallRequirements installs from a requirements.txt at absPath. The path
// is validated to exist and be a regular file before pip is invoked, so
// missing-file errors surface as clean Go errors rather than confusing pip
// stack traces.
func (s *EnvService) InstallRequirements(absPath, indexURL string) error {
	if absPath == "" {
		err := errors.New("requirements path cannot be empty")
		s.logger.Error("InstallRequirements rejected", "error", err.Error(), "source", "backend")
		return err
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		s.logger.Error("InstallRequirements stat failed", "path", absPath, "error", err.Error(), "source", "backend")
		return fmt.Errorf("requirements file: %w", err)
	}
	if fi.IsDir() {
		err := fmt.Errorf("requirements path is a directory: %s", absPath)
		s.logger.Error("InstallRequirements rejected", "error", err.Error(), "source", "backend")
		return err
	}
	if !s.info.Editable {
		err := fmt.Errorf("venv at %s is not writable", s.info.VenvPath)
		s.logger.Error("InstallRequirements rejected", "error", err.Error(), "source", "backend")
		return err
	}
	return s.runInstall("install -r", absPath, indexURL, []string{"-r", absPath})
}

// UninstallPackage removes a single package by name.
func (s *EnvService) UninstallPackage(name string) error {
	if name == "" {
		err := errors.New("package name cannot be empty")
		s.logger.Error("UninstallPackage rejected", "error", err.Error(), "source", "backend")
		return err
	}
	if !s.info.Editable {
		err := fmt.Errorf("venv at %s is not writable", s.info.VenvPath)
		s.logger.Error("UninstallPackage rejected", "error", err.Error(), "source", "backend")
		return err
	}
	pkgArgs := []string{name}
	// pip needs `-y` to skip the prompt; uv pip uninstall doesn't prompt.
	if s.info.ToolName == "pip" {
		pkgArgs = []string{"-y", name}
	}
	return s.runInstall("uninstall", name, "", append([]string{"uninstall"}, pkgArgs...))
}

// runInstall is the streamed-output execution path used by Install /
// InstallRequirements / UninstallPackage. It serializes through s.mu so two
// concurrent operations can't fight over the venv lock file.
func (s *EnvService) runInstall(op, spec, indexURL string, opArgs []string) error {
	if !s.mu.TryLock() {
		err := errors.New("another install/uninstall is already in flight")
		s.logger.Error("runInstall rejected (busy)", "op", op, "error", err.Error(), "source", "backend")
		return err
	}
	defer s.mu.Unlock()
	s.busy.Store(true)
	defer s.busy.Store(false)

	// Build the full command. opArgs already contains the verb for uninstall;
	// for install we add "install" + the spec args.
	var cmdArgs []string
	if strings.HasPrefix(op, "install") {
		cmdArgs = []string{"install"}
		cmdArgs = append(cmdArgs, opArgs...)
		if indexURL != "" {
			cmdArgs = append(cmdArgs, "--index-url="+indexURL)
		}
	} else {
		cmdArgs = opArgs
	}

	args := s.cmdArgs(cmdArgs...)
	s.emit("env:operation:start", map[string]any{"op": op, "spec": spec})

	runErr := s.runStreamed(args)
	if runErr != nil {
		s.logger.Error(op+" failed", "spec", spec, "error", runErr.Error(), "source", "backend")
		s.emit("env:operation:end", map[string]any{"op": op, "spec": spec, "error": runErr.Error()})
		return fmt.Errorf("%s %s: %w", op, spec, runErr)
	}
	s.logger.Info(op+" succeeded", "spec", spec, "source", "backend")
	s.emit("env:operation:end", map[string]any{"op": op, "spec": spec})
	return nil
}

// cmdArgs returns the argv prefix appropriate to the resolved tool.
//   uv:  uv pip <op> --python <python>
//   pip: <python> -m pip <op>
// Caller passes the operation tail (e.g. "install", "pandas").
func (s *EnvService) cmdArgs(tail ...string) []string {
	switch s.info.ToolName {
	case "uv":
		return append([]string{"uv", "pip"}, append(tail, "--python", s.info.PythonPath)...)
	default:
		return append([]string{s.info.PythonPath, "-m", "pip"}, tail...)
	}
}

// runCapture executes argv synchronously and returns its combined stdout.
// stderr is also captured; on non-zero exit, both are folded into the error.
func (s *EnvService) runCapture(argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty argv")
	}
	ctx := context.Background()
	cmd := s.command(ctx, argv[0], argv[1:]...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %s", err.Error(), strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// runStreamed executes argv and forwards stdout+stderr line-by-line as
// env:operation:log events. Returns the exit error, if any.
func (s *EnvService) runStreamed(argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty argv")
	}
	ctx := context.Background()
	cmd := s.command(ctx, argv[0], argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting command: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.forwardLines(stdout, "stdout") }()
	go func() { defer wg.Done(); s.forwardLines(stderr, "stderr") }()
	wg.Wait()

	return cmd.Wait()
}

// forwardLines reads r line-by-line, emitting one env:operation:log event
// per line. EOF and read errors end the loop cleanly.
func (s *EnvService) forwardLines(r io.Reader, stream string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		s.emit("env:operation:log", map[string]any{"stream": stream, "line": scanner.Text()})
	}
}

// command returns an *exec.Cmd, optionally constructed via the test hook.
// Always suppresses the per-subprocess console window on Windows; the Wails
// app is a -H windowsgui binary, so any flash console would be visible.
func (s *EnvService) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if s.commandHook != nil {
		cmd = s.commandHook(ctx, name, args...)
	} else {
		cmd = exec.CommandContext(ctx, name, args...)
	}
	runner.HideConsole(cmd)
	return cmd
}

// emit forwards a payload to the Wails frontend if the app is wired up.
// No-op pre-SetApp (during boot, before app.Run).
func (s *EnvService) emit(event string, payload any) {
	if app := s.app.Load(); app != nil {
		app.Event.Emit(event, payload)
	}
}
