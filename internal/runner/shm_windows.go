package runner

// unlinkShm is a no-op on Windows: the OS reclaims pagefile-backed shared
// memory once the last handle closes, so there is no name to unlink.
func unlinkShm(_ string) {}
