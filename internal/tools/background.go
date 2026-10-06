package tools

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// stopGrace is how long stop waits for a killed process to actually be
// reaped before reporting failure. Killing is asynchronous: taskkill and
// SIGKILL both return before the OS has finished tearing the process
// down, and until it has, the child still holds its open files — which
// is what leaves a workspace directory undeletable after a run.
const stopGrace = 3 * time.Second

// syncBuffer is an io.Writer safe for a running process to write to while
// check_background concurrently reads it.
type syncBuffer struct {
	mu    sync.Mutex
	head  strings.Builder
	tail  strings.Builder
	total int
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	appendBounded(&s.head, &s.tail, &s.total, p)
	return len(p), nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return boundedOutput(s.head.String(), s.tail.String(), s.total)
}

type bgProc struct {
	cmd     *exec.Cmd
	group   *processGroup
	output  *syncBuffer
	exited  bool
	exitErr error
}

// BackgroundProcesses tracks commands started without blocking on them —
// a dev server or watcher that's meant to keep running, not a command
// with a finite exit. Shared by StartBackground/CheckBackground/
// StopBackground; construct one and pass the same pointer to all three.
type BackgroundProcesses struct {
	mu    sync.Mutex
	procs map[string]*bgProc
	next  int
}

func NewBackgroundProcesses() *BackgroundProcesses {
	return &BackgroundProcesses{procs: make(map[string]*bgProc)}
}

// start launches cmd and returns immediately with an id — it does not
// wait for the process to exit. cmd must use a context independent of any
// single tool call's lifetime, or the process would die the instant the
// call that started it returns.
func (p *BackgroundProcesses) start(cmd *exec.Cmd) (id string, output *syncBuffer) {
	buf := &syncBuffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf

	// Wait waits for the I/O copying goroutines as well as the process,
	// so a surviving grandchild that still holds the output pipe blocks
	// it indefinitely — the process is gone and the harness never learns
	// it. WaitDelay bounds that: once the process itself has exited, the
	// pipes are closed and Wait returns rather than hanging on whatever
	// the tree kill failed to reach.
	cmd.WaitDelay = stopGrace

	group := newProcessGroup()
	group.beforeStart(cmd)

	p.mu.Lock()
	p.next++
	id = fmt.Sprintf("bg%d", p.next)
	proc := &bgProc{cmd: cmd, group: group, output: buf}
	p.procs[id] = proc
	p.mu.Unlock()

	if err := cmd.Start(); err != nil {
		group.release()
		p.mu.Lock()
		proc.exited = true
		proc.exitErr = err
		p.mu.Unlock()
		return id, buf
	}
	// A failure to adopt the process is not fatal — the tree kill falls
	// back to walking PIDs, which is what it did before job objects.
	_ = group.afterStart(cmd)

	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		proc.exited = true
		proc.exitErr = err
		p.mu.Unlock()
	}()
	return id, buf
}

func (p *BackgroundProcesses) status(id string) (output string, exited bool, exitErr error, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	proc, ok := p.procs[id]
	if !ok {
		return "", false, nil, fmt.Errorf("unknown background process id %q", id)
	}
	return proc.output.String(), proc.exited, proc.exitErr, nil
}

// stop kills a process tree and waits for it to be gone.
//
// The verdict is whether the process actually exited, never the exit
// status of the tool used to kill it. taskkill reports failure when the
// process has already gone (128) and when tree children vanish
// mid-walk (255); syscall.Kill returns ESRCH for the same case. All of
// those are success for a stop, and treating them as errors made this
// fail intermittently with whichever code the race happened to produce.
func (p *BackgroundProcesses) stop(id string) error {
	p.mu.Lock()
	proc, ok := p.procs[id]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown background process id %q", id)
	}
	if proc.cmd.Process == nil {
		return fmt.Errorf("process %q never started", id)
	}

	killErr := proc.group.kill(proc.cmd)
	if p.waitExited(id, stopGrace) {
		proc.group.release()
		return nil
	}
	if killErr != nil {
		return fmt.Errorf("stopping %s: %w", id, killErr)
	}
	return fmt.Errorf("process %q did not exit within %s of being killed", id, stopGrace)
}

// waitExited reports whether the process has been reaped, polling the
// flag the cmd.Wait goroutine sets.
func (p *BackgroundProcesses) waitExited(id string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		p.mu.Lock()
		proc, ok := p.procs[id]
		exited := ok && proc.exited
		p.mu.Unlock()
		if exited {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// StopAll kills every process this set ever started, for a caller that
// owns the whole set and is tearing it down — the eval harness between
// runs, mainly. A run that leaves a dev server alive holds its temporary
// workspace open, so the next run either starts from a directory that
// won't delete or inherits a server on the same port; both look like
// model failures and are neither.
//
// Errors are ignored — a process that already exited is the expected
// case — but the wait is not: a caller about to delete the workspace
// needs the children to have released their handles first.
func (p *BackgroundProcesses) StopAll() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.procs))
	for id := range p.procs {
		ids = append(ids, id)
	}
	p.mu.Unlock()

	for _, id := range ids {
		_ = p.stop(id)
	}
}
