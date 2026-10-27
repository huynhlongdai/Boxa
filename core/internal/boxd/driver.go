// Package boxd is the host agent: it dials out to core, and executes box
// lifecycle/exec/file/PTY requests through a Driver (incus in production,
// local directories for dev and tests).
package boxd

import (
	"context"
	"io"
	"os/exec"
	"time"

	"boxaz/core/internal/proto"
)

// Driver isolates one box runtime.
type Driver interface {
	Name() string
	Create(ctx context.Context, p proto.CreateParams) error
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]string, error)
	// Run executes command inside the box, streaming output. Returns the exit code.
	Run(ctx context.Context, id string, p proto.ExecParams, stdout, stderr io.Writer) (int, error)
	Read(ctx context.Context, id, path string) (data []byte, isDir bool, entries []proto.Entry, err error)
	Write(ctx context.Context, id, path string, data []byte) error
	Grep(ctx context.Context, id, pattern, path string) (proto.GrepResult, error)
	ChangedFiles(ctx context.Context, id string, since time.Time) ([]string, error)
	Processes(ctx context.Context, id string) ([]proto.Proc, []int, error)
	Snapshot(ctx context.Context, id, snapID string) (location string, sizeBytes int64, err error)
	// Restore restores a snapshot in place or forks it into a new box (p.Create).
	Restore(ctx context.Context, p proto.RestoreParams) error
	// ShellCmd returns an interactive shell command for PTY sessions.
	ShellCmd(ctx context.Context, id string) (*exec.Cmd, error)
	// Addr returns host:port reachable from the host for a port inside the box.
	Addr(ctx context.Context, id string, port int) (string, error)
}

// boxErr builds a protocol error.
func boxErr(code, msg string) *proto.Error { return &proto.Error{Code: code, Message: msg} }
