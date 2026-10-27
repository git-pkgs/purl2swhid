package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/git-pkgs/purl2swhid"
)

func TestCLICustomSWHDoesNotAuthenticate(t *testing.T) {
	t.Setenv("SWH_TOKEN", "offline-token-must-stay-local")
	archive := tarball(t, member{name: "package/hello.txt", data: "hello\n"})
	requests := 0
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("Authorization sent to %s", r.URL.Host)
		}
		switch r.URL.Host {
		case "registry.npmjs.org":
			return response(r, 200, "application/gzip", archive), nil
		case "swh.example":
			requests++
			if r.Method == http.MethodPost {
				return response(r, 200, "application/json", []byte(`{"id":42,"origin_url":"https://registry.npmjs.org/hello/-/hello-1.0.0.tgz","visit_type":"tarball","save_request_status":"accepted"}`)), nil
			}
			return response(r, 404, "application/json", []byte(`{}`)), nil
		default:
			t.Fatalf("unexpected token exchange or request to %s", r.URL)
			return nil, nil
		}
	})}
	cmd := newRoot(client)
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs([]string{"pkg:npm/hello@1.0.0", "--save", "--swh-url=https://swh.example"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("SWH requests=%d", requests)
	}
	if out.String() != treeID("100644", "hello.txt", "hello\n")+"\n" {
		t.Fatalf("stdout=%q", out.String())
	}
	if diagnostics.String() != "Known to Software Heritage: false\nSave request 42: accepted\n" {
		t.Fatalf("stderr=%q", diagnostics.String())
	}
}

func TestResolveRetainsIdentifierOnCheckError(t *testing.T) {
	h := &heritageTransport{t: t, archive: tarball(t, member{name: "package/hello.txt", data: "hello\n"}), status: 503, body: `{}`}
	result, err := purl2swhid.Resolve(context.Background(), "pkg:npm/hello@1.0.0", purl2swhid.Options{Check: true, HTTPClient: &http.Client{Transport: transportFunc(h.roundTrip)}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("err=%v", err)
	}
	if result == nil || result.SWHID != treeID("100644", "hello.txt", "hello\n") || result.Known != nil {
		t.Fatalf("result=%+v", result)
	}
}

func TestCLICheckErrorRetainsPlainOutput(t *testing.T) {
	h := &heritageTransport{t: t, archive: tarball(t, member{name: "package/hello.txt", data: "hello\n"}), status: 429, body: `{}`}
	out, diagnostics, err := execute(t, h.roundTrip, "pkg:npm/hello@1.0.0", "--check")
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || out != treeID("100644", "hello.txt", "hello\n")+"\n" || diagnostics != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, diagnostics, err)
	}
}

func TestCLISaveTaskStatus(t *testing.T) {
	h := &heritageTransport{t: t, archive: tarball(t, member{name: "package/hello.txt", data: "hello\n"}), status: 404, body: `{}`}
	_, diagnostics, err := execute(t, h.roundTrip, "pkg:npm/hello@1.0.0", "--save")
	if err != nil || diagnostics != "Known to Software Heritage: false\nSave request 42: pending (not created)\n" {
		t.Fatalf("stderr=%q err=%v", diagnostics, err)
	}
}
