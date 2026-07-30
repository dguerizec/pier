//go:build !linux

package infra

// The "docker silently drops the port mapping when bind IP isn't on an
// interface yet" race is a Linux-only kernel behaviour. Docker Desktop
// on macOS/Windows runs containers inside a Linux VM whose network stack
// is decoupled from host interfaces, and the sysctl knob doesn't exist there.

func configureNonlocalBind(bindIP string) (bool, error) { return false, nil }

func unconfigureNonlocalBind() (bool, error) { return false, nil }

func needsNonlocalBindRewrite(bindIP string) bool { return false }

func checkNonlocalBind(bindIP string) Check {
	return Check{
		Name:   "kernel allows bind to non-local IP",
		Status: StatusPass,
		Detail: "skipped: only applicable on Linux",
	}
}
