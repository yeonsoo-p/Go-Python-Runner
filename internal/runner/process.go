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
	Params     map[string]string
	GRPCAddr   string
	PythonPath string // optional override for python path

	cmd    *exec.Cmd
	ctx    context.Context
	cancel context.CancelFunc

	stderrMu sync.Mutex
	stderr   bytes.Buffer
}

// NewProcess creates a new process for running a Python script.
func NewProcess(runID, scriptDir string, params map[string]string, grpcAddr string) *Process {
	ctx, cancel := context.WithCancel(context.Background())
	return &Process{
		RunID:     runID,
		ScriptDir: scriptDir,
		Params:    params,
		GRPCAddr:  grpcAddr,
		ctx:       ctx,
		cancel:    cancel,
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

	// Set environment variables for the Python script
	p.cmd.Env = append(os.Environ(),
		"GRPC_ADDRESS="+p.GRPCAddr,
		"RUN_ID="+p.RunID,
		"SCRIPT_DIR="+p.ScriptDir,
	)

	// Capture stderr for crash diagnostics
	stderrPipe, err := p.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	hideConsole(p.cmd)

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// Read stderr in background
	go func() {
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
// 1. .venv/Scripts/python.exe (Windows) or .venv/bin/python3 (Unix) — dev mode
// 2. python/python.exe (Windows) or python/bin/python3 (Unix) — distribution mode
func FindPython() (string, error) {
	// Dev mode: uv-managed venv
	var venvPython string
	if runtime.GOOS == "windows" {
		venvPython = filepath.Join(".venv", "Scripts", "python.exe")
	} else {
		venvPython = filepath.Join(".venv", "bin", "python3")
	}
	if _, err := os.Stat(venvPython); err == nil {
		abs, err := filepath.Abs(venvPython)
		if err != nil {
			return "", fmt.Errorf("resolving venv python path: %w", err)
		}
		return abs, nil
	}

	// Distribution mode: bundled python next to executable
	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
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
