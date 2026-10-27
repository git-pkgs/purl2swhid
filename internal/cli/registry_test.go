package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/git-pkgs/purl2swhid"
)

func TestCLIPyPIArtifactSelection(t *testing.T) {
	archive := tarball(t, member{name: "hello-1.0.0/hello.txt", data: "hello\n"})
	metadata := `{"info":{"name":"hello","version":"1.0.0"},"releases":{"1.0.0":[
		{"filename":"hello.whl","packagetype":"bdist_wheel","url":"https://registry.npmjs.org/hello.whl"},
		{"filename":"hello-1.0.0.tar.gz","packagetype":"sdist","url":"https://registry.npmjs.org/hello-1.0.0.tar.gz"}
	]}}`
	for _, filename := range []string{"", "hello.whl", "absent.tar.gz"} {
		t.Run(filename, func(t *testing.T) {
			checkPyPIArtifact(t, filename, archive, metadata)
		})
	}
}

func checkPyPIArtifact(t *testing.T, filename string, archive []byte, metadata string) {
	t.Helper()
	var downloads []string
	metadataRequests := 0
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "pypi.example" {
			metadataRequests++
			if r.URL.Path != "/pypi/hello/json" {
				t.Fatalf("unexpected metadata request: %s", r.URL)
			}
			return response(r, 200, "application/json", []byte(metadata)), nil
		}
		downloads = append(downloads, r.URL.Path)
		if r.URL.Path == "/hello.whl" {
			return response(r, 200, "application/zip", zipArchive(t, true)), nil
		}
		return response(r, 200, "application/gzip", archive), nil
	})
	args := []string{"pkg:pypi/hello@1.0.0?repository_url=https://pypi.example", "--json"}
	if filename != "" {
		args = append(args, "--filename", filename)
	}
	out, _, err := execute(t, transport, args...)
	if filename == "absent.tar.gz" {
		if err == nil || len(downloads) != 0 {
			t.Fatalf("downloads=%v err=%v", downloads, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var result purl2swhid.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	want := filename
	if want == "" {
		want = "hello-1.0.0.tar.gz"
	}
	if result.Filename != want || len(downloads) != 1 || metadataRequests != 1 {
		t.Fatalf("result=%+v downloads=%v metadata=%d", result, downloads, metadataRequests)
	}
	if result.SWHID != treeID("100644", "hello.txt", "hello\n") {
		t.Fatalf("swhid=%s", result.SWHID)
	}
}

func zipArchive(t *testing.T, unix bool) []byte {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	header := &zip.FileHeader{Name: "hello.txt"}
	if unix {
		header.SetMode(0644)
	}
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "hello\n"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestCLIRejectsUnknownModes(t *testing.T) {
	archive := zipArchive(t, false)
	out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
		return response(r, 200, "application/zip", archive), nil
	}, "pkg:nuget/hello@1.0.0")
	if out != "" || err == nil || !strings.Contains(err.Error(), "Unix file modes") {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
}

func TestCLISaveFailures(t *testing.T) {
	archive := tarball(t, member{name: "package/hello.txt", data: "hello\n"})
	for _, tc := range []struct {
		status     int
		body, want string
	}{
		{403, `{}`, "HTTP 403"},
		{200, `{}`, "invalid save response"},
		{200, `{"id":1,"origin_url":"https://registry.npmjs.org/hello/-/hello-1.0.0.tgz","visit_type":"tarball","save_request_status":"rejected"}`, "rejected"},
		{200, `{"id":1,"origin_url":"https://elsewhere.example/package.tar.gz","visit_type":"tarball","save_request_status":"accepted"}`, "invalid save response"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "registry.npmjs.org" {
					return response(r, 200, "application/gzip", archive), nil
				}
				if r.Method == http.MethodPost {
					return response(r, tc.status, "application/json", []byte(tc.body)), nil
				}
				return response(r, 404, "application/json", []byte(`{}`)), nil
			}, "pkg:npm/hello@1.0.0", "--save")
			if out != treeID("100644", "hello.txt", "hello\n")+"\n" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("stdout=%q err=%v", out, err)
			}
		})
	}
}

func TestCLICheckPlainOutput(t *testing.T) {
	h := &heritageTransport{t: t, archive: tarball(t, member{name: "package/hello.txt", data: "hello\n"}), status: 404, body: `{}`}
	out, diagnostics, err := execute(t, h.roundTrip, "pkg:npm/hello@1.0.0", "--check")
	if err != nil || out != treeID("100644", "hello.txt", "hello\n")+"\n" || diagnostics != "Known to Software Heritage: false\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, diagnostics, err)
	}
}
