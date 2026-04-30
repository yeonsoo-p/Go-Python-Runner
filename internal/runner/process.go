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

type Process struct {
	RunID      string
	ScriptDir  string
	LibDir     string // prepended to PYTHONPATH so scripts can `import runner`
	Params     map[string]string
	GRPCAddr   string
	PythonPath string // optional override

	cmd    *exec.Cmd
	ctx    context.Context
	cancel context.CancelFunc

	stderrMu   sync.Mutex
	stderr     bytes.Buffer
	stderrDone chan struct{} // closed when the stderr reader has drained
}

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

func (p *Process) Start() error {
	pythonPath := p.PythonPath
	if pythonPath == "" {
		v, err := FindVenv()
		if err != nil {
			return fmt.Errorf("finding python: %w", err)
		}
		pythonPath = v.Python
	}

	mainPy := filepath.Join(p.ScriptDir, "main.py")
	p.cmd = exec.CommandContext(p.ctx, pythonPath, mainPy)

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

	stderrPipe, err := p.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	HideConsole(p.cmd)

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// fs.ErrClosed is benign: cancel-before-connect closes the pipe out from
	// under ReadAll. Already-read bytes are still in the buffer; suppress the
	// error so it doesn't surface as a spurious "stderr capture failed".
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

// Wait blocks until the process exits and returns the exit code and captured
// stderr. Cancels p.ctx so Done() observers (e.g. waitAndSendStart) bail
// without burning the connect timeout, and drains p.stderrDone so the buffer
// is fully populated before being read.
func (p *Process) Wait() (exitCode int, stderrOutput string, err error) {
	err = p.cmd.Wait()
	p.cancel()
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

// Cancel terminates the process. Idempotent.
func (p *Process) Cancel() {
	p.cancel()
}

// Done is closed when the process exits, is cancelled, or fails to start.
func (p *Process) Done() <-chan struct{} {
	return p.ctx.Done()
}

// VenvInfo describes the Python environment resolved at startup. Editable is
// false for read-only installs; EnvService consults it before allowing
// install/uninstall.
type VenvInfo struct {
	Root     string
	Python   string
	Editable bool
}

// FindVenv locates the Python venv using the fallback order:
//  1. .venv/ relative to CWD (dev, project root)
//  2. .venv/ relative to executable's parent (dev, bin/ subdirectory)
//  3. python/ relative to executable (distribution, bundled interpreter)
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
