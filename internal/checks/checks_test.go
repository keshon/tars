package checks

import (
	"strings"
	"testing"
)

func TestGofmt_CleanPasses(t *testing.T) {
	if out := Gofmt("a.go", []byte("package a\n")); len(out) != 0 {
		t.Fatalf("clean flagged: %+v", out)
	}
}

func TestGofmt_DirtyPointsAtLine(t *testing.T) {
	out := Gofmt("a.go", []byte("package a\n\nfunc A()  {}\n"))
	if len(out) != 1 || out[0].Rule != "gofmt" || out[0].Line != 3 {
		t.Fatalf("got %+v", out)
	}
}

func TestGofmt_SyntaxError(t *testing.T) {
	out := Gofmt("a.go", []byte("package a\nfunc {\n"))
	if len(out) != 1 || out[0].Rule != "gofmt-syntax" || out[0].Line != 2 {
		t.Fatalf("got %+v", out)
	}
}

func TestSecrets_HitsWithLines(t *testing.T) {
	src := "ok line\nkey = AKIAIOSFODNN7EXAMPLE\n-----BEGIN RSA PRIVATE KEY-----\n"
	out := Secrets("k.env", []byte(src))
	if len(out) != 2 {
		t.Fatalf("got %+v", out)
	}
	if out[0].Line != 2 || out[1].Line != 3 {
		t.Fatalf("lines wrong: %+v", out)
	}
}

func TestSecrets_ProseIsClean(t *testing.T) {
	src := "the secret sauce is patience\napi_key = placeholder\n"
	if out := Secrets("notes.txt", []byte(src)); len(out) != 0 {
		t.Fatalf("prose flagged: %+v", out)
	}
}

func TestScanFile_GoGetsBoth(t *testing.T) {
	out := ScanFile("a.go", []byte("package a\n\nfunc A()  {}\n// AKIAIOSFODNN7EXAMPLE\n"))
	rules := map[string]bool{}
	for _, f := range out {
		rules[f.Rule] = true
	}
	if !rules["gofmt"] || !rules["secret-aws"] {
		t.Fatalf("got %+v", out)
	}
}

func TestScanFile_NonGoSkipsGofmt(t *testing.T) {
	out := ScanFile("a.txt", []byte("x  y\n"))
	for _, f := range out {
		if strings.HasPrefix(f.Rule, "gofmt") {
			t.Fatalf("gofmt on txt: %+v", out)
		}
	}
}

func TestCache_Dedupes(t *testing.T) {
	var c Cache
	f := Finding{Rule: "gofmt", Path: "a.go", Line: 3}
	if got := c.Filter([]Finding{f}); len(got) != 1 {
		t.Fatal("first sighting must pass")
	}
	if got := c.Filter([]Finding{f}); len(got) != 0 {
		t.Fatal("repeat must be suppressed")
	}
	other := Finding{Rule: "gofmt", Path: "a.go", Line: 4}
	if got := c.Filter([]Finding{other}); len(got) != 1 {
		t.Fatal("new line must pass")
	}
}

func TestClamp_CountsDropped(t *testing.T) {
	in := []Finding{{Rule: "a"}, {Rule: "b"}, {Rule: "c"}}
	out, dropped := Clamp(in, 2)
	if len(out) != 2 || dropped != 1 {
		t.Fatalf("out=%v dropped=%d", out, dropped)
	}
	if out, dropped := Clamp(in, 9); len(out) != 3 || dropped != 0 {
		t.Fatalf("no-op clamp broke: %v %d", out, dropped)
	}
}
