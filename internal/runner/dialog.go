package runner

import "errors"

// ErrDialogCancelled signals that the user closed/cancelled a native dialog
// without making a selection. It is NOT a failure — handlers must treat it as
// a clean "no result" outcome and skip reservoir.Report. See § Cancellation
// vs failure in CLAUDE.md.
var ErrDialogCancelled = errors.New("dialog cancelled by user")

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
