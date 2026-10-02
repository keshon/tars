package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// backendStub answers only the endpoints one real backend answers, so a
// probe against it has to identify it the way it would in the field.
func backendStub(t *testing.T, kind string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case kind == "kobold" && r.URL.Path == "/api/extra/true_max_context_length":
			w.Write([]byte(`{"value":32768}`))
		case kind == "llama" && r.URL.Path == "/props":
			w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestDetectKind(t *testing.T) {
	for _, tc := range []struct{ serving, want string }{
		{"kobold", "kobold"},
		{"llama", "llama"},
		{"neither", ""},
	} {
		t.Run(tc.serving, func(t *testing.T) {
			srv := backendStub(t, tc.serving)
			defer srv.Close()
			if got := DetectKind(context.Background(), srv.URL); got != tc.want {
				t.Errorf("DetectKind = %q, want %q", got, tc.want)
			}
		})
	}
}

// A backend that is down must not be reported as the other backend: that
// would send the operator to change a flag that was already correct.
func TestDetectKind_UnreachableIsUnknownNotTheOtherBackend(t *testing.T) {
	srv := backendStub(t, "kobold")
	url := srv.URL
	srv.Close()

	if got := DetectKind(context.Background(), url); got != "" {
		t.Errorf("DetectKind on a closed server = %q, want \"\"", got)
	}
}

// ClientFor is the single place the -backend-kind flag becomes a dialect.
// Every kind it advertises must build, and nothing else may.
func TestClientFor(t *testing.T) {
	kinds := Kinds()
	if len(kinds) == 0 {
		t.Fatal("Kinds() is empty")
	}
	for _, k := range kinds {
		c, err := ClientFor(k, "http://example.invalid", "local")
		if err != nil {
			t.Errorf("ClientFor(%q): %v", k, err)
			continue
		}
		if c.Backend() == "" {
			t.Errorf("ClientFor(%q) built a client with no backend name", k)
		}
	}
	if _, err := ClientFor("ollama", "http://example.invalid", "local"); err == nil {
		t.Error("ClientFor accepted an unknown kind")
	}
}

// The probe DetectKind uses must be the one the client uses, or the
// diagnosis names a backend the client cannot then talk to. Remote
// OpenAI-compatible gateways expose no probe endpoint, so openai is
// undetectable by design and skipped here rather than stubbed.
func TestDetectKindAgreesWithTheClientItRecommends(t *testing.T) {
	for _, kind := range Kinds() {
		if kind == "openai" {
			srv := backendStub(t, "neither")
			defer srv.Close()
			if got := DetectKind(context.Background(), srv.URL); got != "" {
				t.Errorf("openai stub detected as %q, want undetectable", got)
			}
			continue
		}
		srv := backendStub(t, kind)
		detected := DetectKind(context.Background(), srv.URL)
		if detected != kind {
			srv.Close()
			t.Errorf("stub serving %s detected as %q", kind, detected)
			continue
		}
		c, err := ClientFor(detected, srv.URL, "local")
		if err != nil {
			srv.Close()
			t.Fatalf("ClientFor(%q): %v", detected, err)
		}
		n, err := c.MaxContextLength(context.Background())
		srv.Close()
		if err != nil || n != 32768 {
			t.Errorf("%s: recommended client got (%d, %v), want (32768, nil)", kind, n, err)
		}
	}
}
