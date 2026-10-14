package singleton

import "errors"

// ErrAlreadyRunning means another instance already holds the lock.
var ErrAlreadyRunning = errors.New("another AppGuard agent is already running on this computer")
