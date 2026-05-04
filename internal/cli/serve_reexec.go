package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// envInheritedFDs carries the comma-separated fd numbers of listeners
// the parent process passed across the binary swap. Empty/unset means
// we were started fresh and must net.Listen ourselves.
const envInheritedFDs = "PIER_LISTENER_FDS"

// envInheritedAddrs carries the human-readable bind addresses (in the
// same order as the fds). We don't need them to listen — the inherited
// fd is already bound — but they make the start-up log identical
// across fresh starts and re-exec'd starts.
const envInheritedAddrs = "PIER_LISTENER_ADDRS"

// inheritedListeners resolves PIER_LISTENER_FDS into open net.Listeners.
// Returns nil, nil when the env is absent (the caller falls back to
// net.Listen for a fresh start).
func inheritedListeners() ([]net.Listener, []string, error) {
	raw := os.Getenv(envInheritedFDs)
	if raw == "" {
		return nil, nil, nil
	}
	parts := strings.Split(raw, ",")
	addrs := strings.Split(os.Getenv(envInheritedAddrs), ",")
	if len(addrs) != len(parts) {
		return nil, nil, fmt.Errorf("inherited fd/addr count mismatch (%d vs %d)", len(parts), len(addrs))
	}
	listeners := make([]net.Listener, 0, len(parts))
	for i, s := range parts {
		fd, err := strconv.Atoi(s)
		if err != nil {
			return nil, nil, fmt.Errorf("inherited fd %q: %w", s, err)
		}
		f := os.NewFile(uintptr(fd), "pier-listener-"+s)
		ln, err := net.FileListener(f)
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("FileListener fd=%d: %w", fd, err)
		}
		listeners = append(listeners, ln)
		_ = i
	}
	// Cleanse so a re-exec'd grandchild doesn't try to inherit fds
	// that no longer exist.
	os.Unsetenv(envInheritedFDs)
	os.Unsetenv(envInheritedAddrs)
	return listeners, addrs, nil
}

// reexecSelf swaps the current process image with a fresh pier binary
// invocation, passing the existing listener fds via env. We use the
// raw execve syscall (not fork+exec) so the PID stays stable —
// systemd would otherwise see the unit as died+restarted, which
// defeats the whole "graceful upgrade" point.
//
// Trade-off: in-flight HTTP requests die mid-response when the process
// image is replaced. The dashboard's SSE clients reconnect within a
// second so the gap is invisible to humans, but synchronous /api/v1/*
// callers may see one dropped request.
func reexecSelf(listeners []net.Listener, addrs []string) error {
	if len(listeners) != len(addrs) {
		return fmt.Errorf("reexec: listeners/addrs mismatch (%d vs %d)", len(listeners), len(addrs))
	}
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("reexec: locate self: %w", err)
	}

	fds := make([]string, 0, len(listeners))
	for i, ln := range listeners {
		f, err := listenerFile(ln)
		if err != nil {
			return fmt.Errorf("reexec: dup listener %d (%s): %w", i, addrs[i], err)
		}
		// Clear FD_CLOEXEC so the kernel keeps the fd open across
		// execve. (*TCPListener).File() returns a dup with CLOEXEC
		// already set by Go's runtime — we explicitly unset it.
		if err := clearCloexec(f.Fd()); err != nil {
			f.Close()
			return fmt.Errorf("reexec: clear CLOEXEC: %w", err)
		}
		fds = append(fds, strconv.Itoa(int(f.Fd())))
		// Don't f.Close() — that would close the fd we just prepped
		// for the child. We deliberately leak the *os.File wrapper;
		// the runtime is about to be wiped anyway.
	}

	env := append(os.Environ(),
		envInheritedFDs+"="+strings.Join(fds, ","),
		envInheritedAddrs+"="+strings.Join(addrs, ","),
	)
	return syscall.Exec(bin, os.Args, env)
}

// listenerFile pulls the underlying *os.File out of a TCP listener so
// we can hand its fd to a child process. net.FileListener will later
// re-wrap it. We only support TCP — pier doesn't open Unix sockets.
func listenerFile(ln net.Listener) (*os.File, error) {
	type filer interface {
		File() (*os.File, error)
	}
	if f, ok := ln.(filer); ok {
		return f.File()
	}
	return nil, errors.New("listener does not expose File()")
}

func clearCloexec(fd uintptr) error {
	flags, _, e1 := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFD, 0)
	if e1 != 0 {
		return e1
	}
	_, _, e2 := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFD, flags&^syscall.FD_CLOEXEC)
	if e2 != 0 {
		return e2
	}
	return nil
}
