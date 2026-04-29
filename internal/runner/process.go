package runner

import (
	"bytes"
	"context"
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

	hideConsole(p.cmd)

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// Read stderr in background. Close stderrDone after the reader has fully
	// drained the pipe so Wait() can deterministically read p.stderr.
	go func() {
		defer close(p.stderrDone)
		data, err := io.ReadAll(stderrPipe)
		p.stderrMu.Lock()
		if err != nil {
			p.stderr.WriteString(fmt.Sprintf("[stderr capture failed: %v]", err))
		} else {
			p.stderr.Write(data)
		}
		p.stderrMu.Unlock()
	}()

	return nil
}

// Wait blocks until the process exits and returns the exit code and stderr output.
func (p *Process) Wait() (exitCode int, stderrOutput string, err error) {
	err = p.cmd.Wait()
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

// Cancel terminates the process.
func (p *Process) Cancel() {
	p.cancel()
}

// FindPython locates the Python interpreter using the fallback order:
// 1. .venv/ relative to CWD — dev mode (running from project root)
// 2. .venv/ relative to executable — dev mode (binary in bin/ subdirectory)
// 3. python/ relative to executable — distribution mode (bundled interpreter)
func FindPython() (string, error) {
	var venvRel string
	if runtime.GOOS == "windows" {
		venvRel = filepath.Join(".venv", "Scripts", "python.exe")
	} else {
		venvRel = filepath.Join(".venv", "bin", "python3")
	}

	// Dev mode: venv relative to CWD
	if _, err := os.Stat(venvRel); err == nil {
		if abs, err := filepath.Abs(venvRel); err == nil {
			return abs, nil
		}
	}

	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)

		// Dev mode: venv relative to executable's parent (e.g., bin/ -> project root)
		venvFromExec := filepath.Join(execDir, "..", venvRel)
		if _, err := os.Stat(venvFromExec); err == nil {
			if abs, err := filepath.Abs(venvFromExec); err == nil {
				return abs, nil
			}
		}

		// Distribution mode: bundled python next to executable
		var bundledPython string
		if runtime.GOOS == "windows" {
			bundledPython = filepath.Join(execDir, "python", "python.exe")
		} else {
			bundledPython = filepath.Join(execDir, "python", "bin", "python3")
		}
		if _, err := os.Stat(bundledPython); err == nil {
			return bundledPython, nil
		}
	}

	return "", fmt.Errorf("python not found: checked .venv/ and python/ directories")
}
