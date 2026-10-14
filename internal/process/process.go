package process

import "time"

const (
	RunAsAuto    = "auto"
	RunAsUser    = "user"
	RunAsService = "service"
)

// StartOptions describes how an application is launched.
//
// RunAs selects the Windows session: "user" opens the application on the
// interactive desktop of the logged-on user (required for GUI apps when
// AppGuard runs as a service), "service" runs it hidden in session 0, and
// "auto" uses the user session when one is active, otherwise session 0.
type StartOptions struct {
	Path      string
	Dir       string
	Args      []string
	Env       map[string]string
	RunAs     string
	VerifyFor time.Duration
}
