package permission

import "testing"

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
