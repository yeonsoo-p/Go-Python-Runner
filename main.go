package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"go-python-runner/internal/db"
	"go-python-runner/internal/logging"
	"go-python-runner/internal/notify"
	"go-python-runner/internal/registry"
	"go-python-runner/internal/runner"
	"go-python-runner/internal/services"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// version is set at build time via -ldflags "-X main.version=x.y.w.z"
var version = "0.0.0.0"

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Initialize logging. log.Fatalf is fine here: this is the only path
	// that runs before the reservoir exists.
	const ringBufferCapacity = 1000
	ring := logging.NewRingBuffer(ringBufferCapacity)
	logger, err := logging.NewLogger(logging.DefaultLogDir(), ring)
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	slog.SetDefault(logger)

	// Initialize central notification reservoir. Every UI-visible error from
	// Python, Go services, or the frontend goes through this single ingress.
	// See internal/notify/notify.go and CLAUDE.md § Error Handling.
	reservoir := notify.New(logger)

	// Initialize database. log.Fatalf paths still apply here — without a DB
	// there's no point continuing, and the app hasn't booted yet so a toast
	// would not reach a user anyway.
	dsn := os.Getenv("PYRUNNER_DB")
	if dsn == "" {
		dsn, err = db.DefaultDSN()
		if err != nil {
			log.Fatalf("failed to determine database path: %v", err)
		}
	}
	store, err := db.Open(dsn)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}
	reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     "database initialized: " + dsn,
	})

	// Initialize script registry
	reg := registry.New(reservoir)
	scriptsDir := findScriptsDir()
	pluginDir := registry.DefaultPluginDir()
	// Best-effort: ensure the plugin dir exists so the watcher can attach.
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		reservoir.Report(notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Plugin dir unavailable",
			Message:     "could not create plugin dir " + pluginDir,
			Err:         err,
		})
	}
	if err := reg.LoadBuiltin(scriptsDir); err != nil {
		reservoir.Report(notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Builtin scripts load failed",
			Message:     err.Error(),
			Err:         err,
		})
	}
	if err := reg.LoadPlugins(pluginDir); err != nil {
		reservoir.Report(notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Plugin scripts load failed",
			Message:     err.Error(),
			Err:         err,
		})
	}
	reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message: fmt.Sprintf("scripts loaded: count=%d scriptsDir=%s pluginDir=%s issues=%d",
			len(reg.List()), scriptsDir, reg.PluginDir(), len(reg.Issues())),
	})

	// Initialize cache manager
	cache := runner.NewCacheManager()

	// Initialize gRPC server
	grpcServer, err := runner.NewGRPCServer(cache, store, reservoir)
	if err != nil {
		log.Fatalf("failed to start gRPC server: %v", err)
	}
	defer grpcServer.Stop()
	reservoir.Report(notify.Event{
		Severity:    notify.SeverityInfo,
		Persistence: notify.PersistenceOneShot,
		Source:      notify.SourceBackend,
		Message:     "gRPC server started: addr=" + grpcServer.Addr(),
	})

	// Monitor gRPC server for runtime failures
	go func() {
		if err := <-grpcServer.ServeErr(); err != nil {
			reservoir.Report(notify.Event{
				Severity:    notify.SeverityError,
				Persistence: notify.PersistenceOngoing,
				Source:      notify.SourceBackend,
				Key:         "grpc:server:dead",
				Title:       "gRPC server died",
				Message:     "script execution will not work: " + err.Error(),
				Err:         err,
			})
		}
	}()

	// Initialize process manager
	mgr := runner.NewManager(grpcServer, cache, store, reservoir)
	mgr.LibDir = filepath.Join(scriptsDir, "_lib")

	// Create Wails services
	scriptSvc := services.NewScriptService(reg, reservoir, scriptsDir, pluginDir)
	runnerSvc := services.NewRunnerService(mgr, reg, reservoir)
	logSvc := services.NewLogService(ring, reservoir)
	envSvc, envErr := services.NewEnvService(reservoir)
	if envErr != nil {
		// Non-fatal: env management UI is hidden if no venv resolved.
		// Scripts still run via the Manager's own Python resolution.
		reservoir.Report(notify.Event{
			Severity:    notify.SeverityWarn,
			Persistence: notify.PersistenceOneShot,
			Source:      notify.SourceBackend,
			Title:       "Env service unavailable",
			Message:     "environment pane disabled: " + envErr.Error(),
			Err:         envErr,
		})
	}

	// Create Wails application
	wailsServices := []application.Service{
		application.NewService(scriptSvc),
		application.NewService(runnerSvc),
		application.NewService(logSvc),
	}
	if envSvc != nil {
		wailsServices = append(wailsServices, application.NewService(envSvc))
	}
	app := application.New(application.Options{
		Name:        "Go Python Runner",
		Description: "Orchestrates bundled Python scripts through a Go backend",
		Services:    wailsServices,
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	// Give services, reservoir, and gRPC server access to the app.
	// reservoir.SetApp must run before the watcher / gRPC traffic so any
	// early-startup errors actually emit Wails events instead of being
	// dropped silently.
	reservoir.SetApp(app)
	runnerSvc.SetApp(app)
	logSvc.SetApp(app)
	scriptSvc.SetApp(app)
	if envSvc != nil {
		envSvc.SetApp(app)
	}
	grpcServer.SetDialogHandler(&wailsDialogHandler{app: app})

	// Start filesystem watcher: rescans on changes, notifies frontend via
	// scriptSvc.NotifyChanged → scripts:changed Wails event.
	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()
	watcher := registry.NewWatcher(reg, scriptsDir, pluginDir, scriptSvc.NotifyChanged, reservoir)
	go func() {
		if err := watcher.Run(watcherCtx); err != nil {
			reservoir.Report(notify.Event{
				Severity:    notify.SeverityError,
				Persistence: notify.PersistenceOneShot,
				Source:      notify.SourceBackend,
				Title:       "Script watcher exited",
				Message:     err.Error(),
				Err:         err,
			})
		}
	}()

	// Create main window
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Go Python Runner v" + version,
		Width:            1200,
		Height:           800,
		BackgroundColour: application.NewRGB(27, 38, 54),
		URL:              "/",
	})

	// Run the application
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// wailsDialogHandler implements runner.DialogHandler using Wails native dialogs.
type wailsDialogHandler struct {
	app *application.App
}

func (h *wailsDialogHandler) OpenFile(title, directory string, filters []runner.FileFilterDef) (string, error) {
	d := h.app.Dialog.OpenFile().CanChooseFiles(true)
	if title != "" {
		d.SetTitle(title)
	}
	if directory != "" {
		d.SetDirectory(directory)
	}
	for _, f := range filters {
		d.AddFilter(f.DisplayName, f.Pattern)
	}
	path, err := d.PromptForSingleSelection()
	if path == "" {
		// Empty path = user cancelled, regardless of err. Different platforms
		// surface cancel as nil-err vs. a specific err; treat both as cancel.
		return "", runner.ErrDialogCancelled
	}
	return path, err
}

func (h *wailsDialogHandler) SaveFile(title, directory, filename string, filters []runner.FileFilterDef) (string, error) {
	d := h.app.Dialog.SaveFile()
	if title != "" {
		d.SetMessage(title)
	}
	if directory != "" {
		d.SetDirectory(directory)
	}
	if filename != "" {
		d.SetFilename(filename)
	}
	for _, f := range filters {
		d.AddFilter(f.DisplayName, f.Pattern)
	}
	path, err := d.PromptForSingleSelection()
	if path == "" {
		return "", runner.ErrDialogCancelled
	}
	return path, err
}

// findScriptsDir locates the scripts directory relative to the executable or CWD.
func findScriptsDir() string {
	// Check relative to CWD first (dev mode)
	if info, err := os.Stat("scripts"); err == nil && info.IsDir() {
		if abs, err := filepath.Abs("scripts"); err == nil {
			return abs
		}
	}

	// Check relative to executable (distribution mode)
	if execPath, err := os.Executable(); err == nil {
		scriptsDir := filepath.Join(filepath.Dir(execPath), "scripts")
		if info, err := os.Stat(scriptsDir); err == nil && info.IsDir() {
			return scriptsDir
		}
	}

	return "scripts"
}

