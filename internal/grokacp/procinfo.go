package grokacp

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ListenerPID returns the pid listening on bind (host:port) by scanning
// /proc/net/tcp{,6} and /proc/*/fd. Returns 0 when unknown (non-Linux, no
// listener, or the socket belongs to a process this user cannot inspect).
func ListenerPID(bind string) int {
	inode := listenerInode(bind)
	if inode == "" {
		return 0
	}
	target := "socket:[" + inode + "]"
	procs, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fd := range procs {
		if l, err := os.Readlink(fd); err == nil && l == target {
			parts := strings.Split(fd, "/")
			if len(parts) > 2 {
				if pid, err := strconv.Atoi(parts[2]); err == nil {
					return pid
				}
			}
		}
	}
	return 0
}

// PortListening reports whether something accepts TCP connections on bind.
func PortListening(bind string) bool {
	c, err := net.DialTimeout("tcp", bind, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func listenerInode(bind string) string {
	_, portStr, err := net.SplitHostPort(bind)
	if err != nil {
		return ""
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return ""
	}
	want := fmt.Sprintf(":%04X", port)
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			// sl local rem st ... inode at index 9; st 0A == LISTEN
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			if strings.HasSuffix(fields[1], want) {
				fh.Close()
				return fields[9]
			}
		}
		fh.Close()
	}
	return ""
}

// IsOwnGrokAgent reports whether pid is a `grok agent ... serve` process run
// by the current user (so a reset may stop it).
func IsOwnGrokAgent(pid int) bool {
	st, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return false
	}
	if uid, ok := statUID(st); !ok || uid != os.Getuid() {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	cmd := strings.ReplaceAll(string(b), "\x00", " ")
	return strings.Contains(cmd, "grok") && strings.Contains(cmd, " agent ") && strings.Contains(cmd, " serve")
}

// ProcessUser returns the numeric uid owning pid, or -1.
func ProcessUser(pid int) int {
	st, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return -1
	}
	if uid, ok := statUID(st); ok {
		return uid
	}
	return -1
}

func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func waitPortFree(bind string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !PortListening(bind) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}
