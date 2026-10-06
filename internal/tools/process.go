package tools

import (
	"context"
	"os"
	"strings"
	"time"
)

// RunCommand is the common bounded, credential-scrubbed shell path for tools
// and verification. Cancellation kills the process tree and bounds pipe drain.
func RunCommand(ctx context.Context, command, dir string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := shellCommand(ctx, command)
	cmd.Dir = dir
	cmd.Env = scrubEnv(os.Environ())
	cmd.WaitDelay = stopGrace
	group := newProcessGroup()
	defer group.release()
	group.beforeStart(cmd)
	cmd.Cancel = func() error { return group.kill(cmd) }
	out := &syncBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	if err := group.afterStart(cmd); err != nil {
		_ = group.kill(cmd)
		_ = cmd.Wait()
		return out.String(), err
	}
	err := cmd.Wait()
	_ = group.kill(cmd)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out.String(), err
}

// Keep both the beginning and recent tail without retaining unbounded logs.
func boundedOutput(head, tail string, total int) string {
	if total <= shellMaxBytes {
		return head + tail
	}
	return head + "\n...(output truncated; recent tail follows)\n" + tail
}

func appendBounded(head, tail *strings.Builder, total *int, p []byte) {
	*total += len(p)
	half := shellMaxBytes / 2
	if head.Len() < half {
		n := half - head.Len()
		if n > len(p) {
			n = len(p)
		}
		head.Write(p[:n])
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	if len(p) >= half {
		tail.Reset()
		tail.Write(p[len(p)-half:])
		return
	}
	previous := tail.String()
	if len(previous)+len(p) > half {
		previous = previous[len(previous)+len(p)-half:]
	}
	tail.Reset()
	tail.WriteString(previous)
	tail.Write(p)
}
