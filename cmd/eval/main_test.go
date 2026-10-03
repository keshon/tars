package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeProbe(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOsSkipReason(t *testing.T) {
	if skip, _ := osSkipReason(nil); skip {
		t.Error("empty oses must run everywhere")
	}
	if skip, _ := osSkipReason([]string{runtime.GOOS}); skip {
		t.Errorf("host OS %s must not skip", runtime.GOOS)
	}
	other := "windows"
	if runtime.GOOS == "windows" {
		other = "linux"
	}
	skip, reason := osSkipReason([]string{other})
	if !skip || !strings.Contains(reason, other) {
		t.Errorf("want skip for %s, got %v %q", other, skip, reason)
	}
}

func TestLoadProbes_RejectsBadOses(t *testing.T) {
	dir := t.TempDir()
	writeProbe(t, dir, "x.json", `{"task":"t","tier":"smoke","oses":["plan9"],
		"trace":[{"tool":"read_file","mode":"required"}]}`)
	if _, err := loadProbes(dir, ""); err == nil ||
		!strings.Contains(err.Error(), "oses") {
		t.Fatalf("want oses rejection, got %v", err)
	}
}

func TestLoadProbes_AcceptsOsesAndNewCheckType(t *testing.T) {
	dir := t.TempDir()
	writeProbe(t, dir, "x.json", `{"task":"t","tier":"smoke","oses":["windows","linux"],
		"verify":[{"type":"file_absent","path":"gone.txt"}],
		"trace":[{"tool":"write_file","mode":"required"}]}`)
	probes, err := loadProbes(dir, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(probes) != 1 || len(probes[0].Oses) != 2 {
		t.Fatalf("probes=%+v", probes)
	}
	if probes[0].Verify[0].Type != "file_absent" {
		t.Fatalf("verify=%+v", probes[0].Verify)
	}
}

func TestLoadProbes_RejectsBadVerify(t *testing.T) {
	dir := t.TempDir()
	writeProbe(t, dir, "x.json", `{"task":"t","tier":"smoke",
		"verify":[{"type":"content_containz","path":"a","contains":"b"}]}`)
	if _, err := loadProbes(dir, ""); err == nil ||
		!strings.Contains(err.Error(), "verify[0]") {
		t.Fatalf("want verify rejection, got %v", err)
	}
	writeProbe(t, dir, "y.json", `{"task":"t","tier":"smoke",
		"verify":[{"type":"content_contains","path":"a"}]}`)
	if _, err := loadProbes(dir, ""); err == nil ||
		!strings.Contains(err.Error(), "verify[") {
		t.Fatalf("want missing-field rejection, got %v", err)
	}
}

func TestLoadProbes_RejectsBadRegex(t *testing.T) {
	dir := t.TempDir()
	writeProbe(t, dir, "x.json", `{"task":"t","tier":"smoke",
		"trace":[{"tool":"read_file","args_regex":"([unclosed"}]}`)
	if _, err := loadProbes(dir, ""); err == nil ||
		!strings.Contains(err.Error(), "args_regex") {
		t.Fatalf("want regex rejection, got %v", err)
	}
}
