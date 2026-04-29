package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// Process represents a single Python subprocess.
type Process struct {
	RunID      string
	ScriptDir  string
	LibDir     string // path to scripts/_lib, prepended to PYTHONPATH so scripts can `import runner`
	Params     map[string]string
	GRPCAddr   string
	PythonPath string // optional override for python path

	cmd    *exec.Cmd
	ctx    context.Context
	cancel context.CancelFunc

	stderrMu   sync.Mutex
	stderr     bytes.Buffer
	stderrDone chan struct{} // closed when the stderr reader goroutine has fully drained the pipe
}

// NewProcess creates a new process for running a Python script.
func NewProcess(runID, scriptDir, libDir string, params map[string]string, grpcAddr string) *Process {
	ctx, cancel := context.WithCancel(context.Background())
	return &Process{
		RunID:      runID,
		ScriptDir:  scriptDir,
		LibDir:     libDir,
		Params:     params,
		GRPCAddr:   grpcAddr,
		ctx:        ctx,
		cancel:     cancel,
		stderrDone: make(chan struct{}),
	}
}

// Start spawns the Python subprocess.
func (p *Process) Start() error {
	pythonPath := p.PythonPath
	if pythonPath == "" {
		var err error
		pythonPath, err = FindPython()
		if err != nil {
			return fmt.Errorf("finding python: %w", err)
		}
	}

	mainPy := filepath.Join(p.ScriptDir, "main.py")
	p.cmd = exec.CommandContext(p.ctx, pythonPath, mainPy)

	// Set environment variables for the Python script. PYTHONPATH includes the
	// runner helper library directory so scripts (including user-installed
	// plugins) can `import runner` without sys.path manipulation.
	env := append(os.Environ(),
		"GRPC_ADDRESS="+p.GRPCAddr,
		"RUN_ID="+p.RunID,
	)
	if p.LibDir != "" {
		existing := os.Getenv("PYTHONPATH")
		if existing == "" {
			env = append(env, "PYTHONPATH="+p.LibDir)
		} else {
			env = append(env, "PYTHONPATH="+p.LibDir+string(os.PathListSeparator)+existing)
		}
	}
	p.cmd.Env = env

	// Capture stderr for crash diagnostics
	stderrPipe, err := p.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	HideConsole(p.cmd)

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// Read stderr in background. Close stderrDone after the reader has fully
	// drained the pipe so Wait() can deterministically read p.stderr.
	//
	// "file already closed" / fs.ErrClosed is benign: when the process is killed
	// (e.g. cancel-before-connect), the OS closes the pipe externally and our
	// in-flight ReadAll observes that close. Any bytes already read are still in
	// the buffer; suppress the error to avoid a noisy "stderr capture failed"
	// log for an event that isn't a capture failure.
	go func() {
		defer close(p.stderrDone)
		data, err := io.ReadAll(stderrPipe)
		p.stderrMu.Lock()
		p.stderr.Write(data)
		if err != nil && !errors.Is(err, os.ErrClosed) {
			p.stderr.WriteString(fmt.Sprintf("[stderr capture failed: %v]", err))
		}
		p.stderrMu.Unlock()
	}()

	return nil
}

// Wait blocks until the process exits and returns the exit code and stderr output.
func (p *Process) Wait() (exitCode int, stderrOutput string, err error) {
	err = p.cmd.Wait()
	// Cancel the context so Done() observers (e.g. waitAndSendStart waiting
	// for Python to connect) can bail without burning the full connectTimeout.
	// p.cancel is idempotent.
	p.cancel()
	// Wait for the stderr reader to finish draining the pipe before reading
	// the buffer; cmd.Wait() returning does not by itself guarantee the reader
	// has flushed.
	<-p.stderrDone
	p.stderrMu.Lock()
	stderrOutput = p.stderr.String()
	p.stderrMu.Unlock()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), stderrOutput, err
		}
		return -1, stderrOutput, err
	}
	return 0, stderrOutput, nil
}

// Cancel terminates the process. Safe to call multiple times; safe to call
// after the process has already exited (no-op).
func (p *Process) Cancel() {
	p.cancel()
}

// Done returns a channel that is closed when the process exits, is cancelled,
// or fails to start. Other goroutines (e.g. those waiting for the Python
// client to connect via gRPC) can select on this to bail early instead of
// waiting on a long timeout for an event that will never happen.
func (p *Process) Done() <-chan struct{} {
	return p.ctx.Done()
}

// VenvInfo describes the Python environment the app resolved at startup.
// Editable reflects whether the venv root is writable — false for read-only
// installs. EnvService consults this before allowing install/uninstall.
type VenvInfo struct {
	Root     string // venv root directory (parent of Scripts/ or bin/)
	Python   string // absolute path to the Python interpreter
	Editable bool
}

// FindVenv locates the Python venv using the fallback order:
// 1. .venv/ relative to CWD — dev mode (running from project root)
// 2. .venv/ relative to executable — dev mode (binary in bin/ subdirectory)
// 3. python/ relative to executable — distribution mode (bundled interpreter)
//
// Returns the venv root and interpreter path. EnvService and the process
// manager share this resolver so both operate on the same Python.
func FindVenv() (VenvInfo, error) {
	var pythonRel string
	if runtime.GOOS == "windows" {
		pythonRel = filepath.Join("Scripts", "python.exe")
	} else {
		pythonRel = filepath.Join("bin", "python3")
	}

	// Dev mode: .venv relative to CWD.
	if root, err := filepath.Abs(".venv"); err == nil {
		if _, err := os.Stat(filepath.Join(root, pythonRel)); err == nil {
			return VenvInfo{Root: root, Python: filepath.Join(root, pythonRel), Editable: isWritable(root)}, nil
		}
	}

	if execPath, err := os.Executable(); err == nil {
		execDir := filepath.Dir(execPath)

		// Dev mode: .venv relative to executable's parent.
		if root, err := filepath.Abs(filepath.Join(execDir, "..", ".venv")); err == nil {
			if _, err := os.Stat(filepath.Join(root, pythonRel)); err == nil {
				return VenvInfo{Root: root, Python: filepath.Join(root, pythonRel), Editable: isWritable(root)}, nil
			}
		}

		// Distribution mode: bundled python next to executable.
		bundled := filepath.Join(execDir, "python")
		if _, err := os.Stat(filepath.Join(bundled, pythonRel)); err == nil {
			return VenvInfo{Root: bundled, Python: filepath.Join(bundled, pythonRel), Editable: isWritable(bundled)}, nil
		}
	}

	return VenvInfo{}, fmt.Errorf("python not found: checked .venv/ and python/ directories")
}

// FindPython is the legacy wrapper. New code should call FindVenv directly so
// it has access to the venv root and editability.
func FindPython() (string, error) {
	v, err := FindVenv()
	if err != nil {
		return "", err
	}
	return v.Python, nil
}

// isWritable returns true if the directory accepts new files. Used to gate
// EnvService install/uninstall on the read-only-install case.
func isWritable(dir string) bool {
	tmp, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	return true
}
