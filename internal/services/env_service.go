package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"go-python-runner/internal/notify"
	"go-python-runner/internal/runner"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type EnvInfo struct {
	PythonPath    string `json:"pythonPath"`
	PythonVersion string `json:"pythonVersion"`
	VenvPath      string `json:"venvPath"`
	ToolName      string `json:"toolName"` // "uv" or "pip"
	Editable      bool   `json:"editable"`
}

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// EnvService inspects and mutates the resolved venv. Install/uninstall
// serialize via s.op CAS; ListPackages is lock-free.
type EnvService struct {
	info        EnvInfo
	op          atomic.Int32 // envOpState
	reservoir   notify.Reservoir
	app         atomic.Pointer[application.App]
	commandHook commandHook
}

type commandHook func(ctx context.Context, name string, args ...string) *exec.Cmd

const (
	envOpIdle    int32 = 0
	envOpRunning int32 = 1
)

// tryEnter atomically transitions idle → running and emits env:operation:start
// inside the transition so observers see EnvBusy()=true and the start event in
// agreement. Returns false if another op is already in flight.
func (s *EnvService) tryEnter(op, spec string) bool {
	if !s.op.CompareAndSwap(envOpIdle, envOpRunning) {
		return false
	}
	s.emit("env:operation:start", map[string]any{"op": op, "spec": spec})
	return true
}

// exit emits env:operation:end and transitions running → idle. Pairs with
// tryEnter; must only be called after a successful tryEnter.
func (s *EnvService) exit(op, spec string) {
	s.emit("env:operation:end", map[string]any{"op": op, "spec": spec})
	s.op.Store(envOpIdle)
}

// NewEnvService probes the venv and tooling. Returns an error only if there
// is no venv at all; an unwritable venv yields Editable=false and the
// install/uninstall paths fail-fast with a clear error.
func NewEnvService(reservoir notify.Reservoir) (*EnvService, error) {
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
	// Best-effort version probe; informational only.
	{
		probe := exec.Command(venv.Python, "--version")
		runner.HideConsole(probe)
		if out, err := probe.CombinedOutput(); err == nil {
			info.PythonVersion = strings.TrimSpace(string(out))
		}
	}

	reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message: fmt.Sprintf("env service initialized: python=%s venv=%s tool=%s version=%s editable=%t",
			info.PythonPath, info.VenvPath, info.ToolName, info.PythonVersion, info.Editable),
	})

	return &EnvService{info: info, reservoir: reservoir}, nil
}

func (s *EnvService) SetApp(app *application.App) {
	s.app.Store(app)
}

func (s *EnvService) GetEnvInfo() EnvInfo {
	return s.info
}

// EnvBusy reports whether an install/uninstall is in flight. Reads the same
// state that env:operation:start / env:operation:end events transition.
func (s *EnvService) EnvBusy() bool {
	return s.op.Load() == envOpRunning
}

func (s *EnvService) ListPackages() ([]Package, error) {
	args := s.cmdArgs("list", "--format=json")
	out, err := s.runCapture(args)
	if err != nil {
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "ListPackages failed",
			Message:     err.Error(),
			Err:         err,
		})
		return nil, fmt.Errorf("listing packages: %w", err)
	}
	var pkgs []Package
	if err := json.Unmarshal(out, &pkgs); err != nil {
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "ListPackages parse failed",
			Message:     err.Error(),
			Err:         err,
		})
		return nil, fmt.Errorf("parsing pip list output: %w", err)
	}
	return pkgs, nil
}

// InstallPackage installs a single spec (e.g. "pandas", "numpy>=2.0",
// "git+https://..."). Output streams as env:operation:log events.
func (s *EnvService) InstallPackage(spec, indexURL string) error {
	if spec == "" {
		err := errors.New("install spec cannot be empty")
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "InstallPackage rejected",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}
	if !s.info.Editable {
		err := fmt.Errorf("venv at %s is not writable", s.info.VenvPath)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "InstallPackage rejected",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}
	return s.runInstall("install", spec, indexURL, []string{spec})
}

// InstallRequirements installs from a requirements.txt at absPath. The path
// is validated before pip runs so missing-file errors surface cleanly rather
// than as a pip stack trace.
func (s *EnvService) InstallRequirements(absPath, indexURL string) error {
	if absPath == "" {
		err := errors.New("requirements path cannot be empty")
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "InstallRequirements rejected",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "InstallRequirements stat failed",
			Message:     fmt.Sprintf("path=%s: %s", absPath, err.Error()),
			Err:         err,
		})
		return fmt.Errorf("requirements file: %w", err)
	}
	if fi.IsDir() {
		err := fmt.Errorf("requirements path is a directory: %s", absPath)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "InstallRequirements rejected",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}
	if !s.info.Editable {
		err := fmt.Errorf("venv at %s is not writable", s.info.VenvPath)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "InstallRequirements rejected",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}
	return s.runInstall("install -r", absPath, indexURL, []string{"-r", absPath})
}

func (s *EnvService) UninstallPackage(name string) error {
	if name == "" {
		err := errors.New("package name cannot be empty")
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "UninstallPackage rejected",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}
	if !s.info.Editable {
		err := fmt.Errorf("venv at %s is not writable", s.info.VenvPath)
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "UninstallPackage rejected",
			Message:     err.Error(),
			Err:         err,
		})
		return err
	}
	pkgArgs := []string{name}
	// pip needs `-y` to skip the prompt; uv pip uninstall doesn't prompt.
	if s.info.ToolName == "pip" {
		pkgArgs = []string{"-y", name}
	}
	return s.runInstall("uninstall", name, "", append([]string{"uninstall"}, pkgArgs...))
}

// runInstall is the streamed-output path shared by install / install -r /
// uninstall. env:operation:start/end are lifecycle signals; errors flow
// through the reservoir.
func (s *EnvService) runInstall(op, spec, indexURL string, opArgs []string) error {
	if !s.tryEnter(op, spec) {
		err := errors.New("another install/uninstall is already in flight")
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Operation busy",
			Message:     fmt.Sprintf("op=%s: %s", op, err.Error()),
			Err:         err,
		})
		return err
	}
	defer s.exit(op, spec)

	// opArgs already contains the verb for uninstall; install prepends it.
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

	runErr := s.runStreamed(args)
	if runErr != nil {
		s.reservoir.Report(notify.Event{
			Severity:    notify.SeverityError,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       op + " failed",
			Message:     fmt.Sprintf("%s %s: %s", op, spec, runErr.Error()),
			Err:         runErr,
		})
		return fmt.Errorf("%s %s: %w", op, spec, runErr)
	}
	s.reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     fmt.Sprintf("%s succeeded: spec=%s", op, spec),
	})
	return nil
}

// cmdArgs returns the argv prefix for the resolved tool:
//
//	uv:  uv pip <tail...> --python <python>
//	pip: <python> -m pip <tail...>
func (s *EnvService) cmdArgs(tail ...string) []string {
	switch s.info.ToolName {
	case "uv":
		return append([]string{"uv", "pip"}, append(tail, "--python", s.info.PythonPath)...)
	default:
		return append([]string{s.info.PythonPath, "-m", "pip"}, tail...)
	}
}

// runCapture runs argv and returns stdout. On non-zero exit, stderr is
// folded into the error.
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
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// runStreamed runs argv and forwards stdout+stderr line-by-line as
// env:operation:log events.
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

func (s *EnvService) forwardLines(r io.Reader, stream string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		s.emit("env:operation:log", map[string]any{"stream": stream, "line": scanner.Text()})
	}
}

// command returns an *exec.Cmd via the test hook if set, otherwise
// exec.CommandContext. HideConsole suppresses the flash console on Windows
// (the Wails app is -H windowsgui).
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

func (s *EnvService) emit(event string, payload any) {
	if app := s.app.Load(); app != nil {
		app.Event.Emit(event, payload)
	}
}
