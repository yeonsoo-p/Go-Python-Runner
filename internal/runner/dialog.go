package runner

import "errors"

// ErrDialogCancelled signals that the user closed/cancelled a native dialog
// without making a selection. It is NOT a failure — handlers must treat it as
// a clean "no result" outcome and skip reservoir.Report. See § Cancellation
// vs failure in CLAUDE.md.
var ErrDialogCancelled = errors.New("dialog cancelled by user")

// ErrRunNotActive signals that CancelRun was called for a runID that is not
// in the manager's activeRuns map — typically because the run already
// terminated (organic failure, normal completion) or was never registered.
// It is NOT a cancel failure: there's nothing to cancel. CancelGroup filters
// this sentinel via errors.Is so partial group cancels don't surface spurious
// "did not cancel" toasts for siblings that finished on their own. Mirrors
// the ErrDialogCancelled boundary-sentinel pattern.
var ErrRunNotActive = errors.New("run not active")

// DialogHandler opens native file dialogs. Implemented by the Wails app layer
// in main.go. The interface boundary is where empty-path / cancel-error /
// "no selection" platform variations get translated to ErrDialogCancelled —
// the gRPC handler does not need to know the underlying dialog library.
type DialogHandler interface {
	OpenFile(title string, directory string, filters []FileFilterDef) (string, error)
	SaveFile(title string, directory string, filename string, filters []FileFilterDef) (string, error)
}

// FileFilterDef describes a file type filter for dialogs.
type FileFilterDef struct {
	DisplayName string
	Pattern     string
}
