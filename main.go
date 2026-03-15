package main

import (
	"embed"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"go-python-runner/internal/db"
	"go-python-runner/internal/logging"
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
	// Initialize logging
	ring := logging.NewRingBuffer(1000)
	logger, err := logging.NewLogger(logging.DefaultLogDir(), ring)
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	slog.SetDefault(logger)

	// Initialize database
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
	logger.Info("database initialized", "dsn", dsn, "source", "backend")

	// Initialize script registry
	reg := registry.New(logger)
	scriptsDir := findScriptsDir()
	if err := reg.LoadBuiltin(scriptsDir); err != nil {
		logger.Warn("failed to load builtin scripts", "error", err.Error())
	}
	if err := reg.LoadPlugins(registry.DefaultPluginDir()); err != nil {
		logger.Warn("failed to load plugin scripts", "error", err.Error())
	}
	logger.Info("scripts loaded",
		"count", len(reg.List()),
		"scriptsDir", scriptsDir,
		"pluginDir", reg.PluginDir(),
		"source", "backend",
	)

	// Initialize cache manager
	cache := runner.NewCacheManager()

	// Initialize gRPC server
	grpcServer, err := runner.NewGRPCServer(cache, store, logger)
	if err != nil {
		log.Fatalf("failed to start gRPC server: %v", err)
	}
	defer grpcServer.Stop()
	logger.Info("gRPC server started", "addr", grpcServer.Addr(), "source", "backend")

	// Initialize process manager
	mgr := runner.NewManager(grpcServer, cache, logger)

	// Create Wails services
	scriptSvc := services.NewScriptService(reg)
	runnerSvc := services.NewRunnerService(mgr, reg, logger)
	logSvc := services.NewLogService(logger, ring)

	// Create Wails application
	app := application.New(application.Options{
		Name:        "Go Python Runner",
		Description: "Orchestrates bundled Python scripts through a Go backend",
		Services: []application.Service{
			application.NewService(scriptSvc),
			application.NewService(runnerSvc),
			application.NewService(logSvc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	// Give services and gRPC server access to the app
	runnerSvc.SetApp(app)
	logSvc.SetApp(app)
	grpcServer.SetDialogHandler(&wailsDialogHandler{app: app})

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
	return d.PromptForSingleSelection()
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
	return d.PromptForSingleSelection()
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
