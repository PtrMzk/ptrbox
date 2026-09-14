package cli

// The real fetch, against a loopback server shaped like LM Studio's.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func lmstudioServer(t *testing.T, status int, body string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

func TestFetchLMStudioModelsListsChatModelsOnly(t *testing.T) {
	port := lmstudioServer(t, 200, `{"object":"list","data":[
		{"id":"qwen/qwen3-coder-30b","object":"model"},
		{"id":"text-embedding-nomic-embed-text-v1.5","object":"model"},
		{"id":"google/gemma-3-12b","object":"model"}]}`)
	got, err := fetchLMStudioModels(port)
	if err != nil {
		t.Fatal(err)
	}
	if want := "qwen/qwen3-coder-30b google/gemma-3-12b"; strings.Join(got, " ") != want {
		t.Errorf("models = %q, want %q (embedding model dropped, order kept)", got, want)
	}
}

func TestFetchLMStudioModelsFailsOnAnythingButAModelList(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"a server error": {500, "boom"},
		"not json":       {200, "<html>"},
	} {
		t.Run(name, func(t *testing.T) {
			port := lmstudioServer(t, tc.status, tc.body)
			if _, err := fetchLMStudioModels(port); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestFetchLMStudioModelsNamesTheAddressWhenNothingListens(t *testing.T) {
	// Bind and release a port so nothing is on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	_, err = fetchLMStudioModels(port)
	if err == nil {
		t.Fatal("no error for a closed port")
	}
}

func TestOpencodeModelsJSONIsOneSortedLine(t *testing.T) {
	got := opencodeModelsJSON([]string{"zeta", "alpha@q4_k_m"})
	if want := `{"alpha@q4_k_m":{"name":"alpha@q4_k_m"},"zeta":{"name":"zeta"}}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
	if got := opencodeModelsJSON(nil); got != "{}" {
		t.Errorf("json for no models = %s, want {}", got)
	}
}

func TestModelIDCharset(t *testing.T) {
	for _, ok := range []string{"qwen/qwen3-coder-30b", "gemma-3-12b@q4_k_m", "a.b:c_d", "Model7"} {
		if err := validModelID(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "a__b", `a"b`, "a$b", "a;b", "a\nb", "/abs"} {
		if err := validModelID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
