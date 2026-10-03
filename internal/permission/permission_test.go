package permission

import (
	"strings"
	"testing"
)

func TestDefaultAsksEnv(t *testing.T) {
	p := Default()
	if got := p.Evaluate("read_file", ".env"); got != Ask {
		t.Errorf("read .env = %v, want ask", got)
	}
	if got := p.Evaluate("read_file", "config.go"); got != Allow {
		t.Errorf("read config.go = %v, want allow", got)
	}
}

func TestLastMatchWins(t *testing.T) {
	p := Default().WithExtra([]Rule{{Tool: "read_file", Pattern: ".env", Effect: Deny}})
	if got := p.Evaluate("read_file", ".env"); got != Deny {
		t.Errorf("override = %v, want deny", got)
	}
}

func TestUnknownDefaultsAllow(t *testing.T) {
	p := Default()
	if got := p.Evaluate("run_shell", "go test ./..."); got != Allow {
		t.Errorf("shell = %v, want allow", got)
	}
}

func TestDestructiveShellAsks(t *testing.T) {
	p := Default()
	for _, cmd := range []string{"rm -rf /tmp/x", "del /s /q build", "sudo mkfs.ext4 /dev/sda1", "dd if=/dev/zero of=x"} {
		if got := p.Evaluate("run_shell", cmd); got != Ask {
			t.Errorf("shell %q = %v, want ask", cmd, got)
		}
		if got := p.Evaluate("start_background", cmd); got != Ask {
			t.Errorf("background %q = %v, want ask", cmd, got)
		}
	}
	for _, cmd := range []string{"go test ./...", "npm run dev", "git status"} {
		if got := p.Evaluate("run_shell", cmd); got != Allow {
			t.Errorf("shell %q = %v, want allow", cmd, got)
		}
	}
}

func TestDecide_Answers(t *testing.T) {
	for _, ans := range []string{"y", "Y", "yes", "a", "always"} {
		if eff, err := Decide(ans); eff != Allow || err != nil {
			t.Errorf("Decide(%q) = %v, %v; want allow", ans, eff, err)
		}
	}
	if eff, err := Decide("n"); eff != Deny || err == nil {
		t.Errorf("Decide(n) = %v, %v; want deny", eff, err)
	}
}

func TestDecide_RejectNote(t *testing.T) {
	for _, ans := range []string{"n: use read_file instead", "no: too broad", "deny: not now"} {
		eff, err := Decide(ans)
		if eff != Deny || err == nil {
			t.Fatalf("Decide(%q) = %v, %v; want deny", ans, eff, err)
		}
		if !strings.Contains(err.Error(), strings.TrimSpace(strings.SplitN(ans, ":", 2)[1])) {
			t.Errorf("Decide(%q) dropped the note: %v", ans, err)
		}
	}
	// Bare "n:" with no note denies generically, like "n".
	if _, err := Decide("n:"); err == nil || err.Error() != "blocked by operator" {
		t.Errorf("Decide(n:) = %v, want plain denial", err)
	}
}
