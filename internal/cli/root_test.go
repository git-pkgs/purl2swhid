package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/git-pkgs/purl2swhid"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type member struct {
	name, data, link string
	mode             int64
	kind             byte
}

func tarball(t *testing.T, members ...member) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		kind := m.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := m.mode
		if mode == 0 {
			mode = 0644
		}
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Size: int64(len(m.data)), Mode: mode, Typeflag: kind, Linkname: m.link}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func response(request *http.Request, status int, contentType string, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: request}
}

func execute(t *testing.T, transport transportFunc, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("SWH_TOKEN", "")
	cmd := newRoot(&http.Client{Transport: transport})
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), diagnostics.String(), err
}

func objectHash(kind string, data []byte) string {
	h := sha1.New()
	h.Write(fmt.Appendf(nil, "%s %d\x00", kind, len(data)))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func treeID(mode, name, content string) string {
	hash, _ := hex.DecodeString(objectHash("blob", []byte(content)))
	data := append([]byte(mode+" "+name+"\x00"), hash...)
	return "swh:1:dir:" + objectHash("tree", data)
}

func wrappedTreeID(name, childID string) string {
	hash, _ := hex.DecodeString(strings.TrimPrefix(childID, "swh:1:dir:"))
	return "swh:1:dir:" + objectHash("tree", append([]byte("40000 "+name+"\x00"), hash...))
}

func TestCLIHashesPackage(t *testing.T) {
	archive := tarball(t, member{name: "package/hello.txt", data: "hello\n"})
	var requests []string
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		return response(r, http.StatusOK, "application/gzip", archive), nil
	})
	out, diagnostics, err := execute(t, transport, "pkg:npm/hello@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	want := treeID("100644", "hello.txt", "hello\n") + "\n"
	if out != want || diagnostics != "" {
		t.Fatalf("stdout=%q stderr=%q, want %q", out, diagnostics, want)
	}
	if len(requests) != 1 || requests[0] != "https://registry.npmjs.org/hello/-/hello-1.0.0.tgz" {
		t.Fatalf("requests=%v", requests)
	}
}

func TestCLIArchiveModesAndPaths(t *testing.T) {
	for _, tc := range []struct {
		name, purl string
		members    []member
		args       []string
		want, root string
	}{
		{name: "executable", purl: "pkg:npm/hello@1.0.0", members: []member{{name: "package/run", data: "hello\n", mode: 0755}}, want: treeID("100755", "run", "hello\n"), root: "package"},
		{name: "symlink", purl: "pkg:npm/hello@1.0.0", members: []member{{name: "package/link", kind: tar.TypeSymlink, link: "../target"}}, want: treeID("120000", "link", "../target"), root: "package"},
		{name: "file subpath", purl: "pkg:npm/hello@1.0.0#hello.txt", members: []member{{name: "package/hello.txt", data: "hello\n"}}, want: "swh:1:cnt:ce013625030ba8dba906f756967f9e9ca394464a", root: "package/hello.txt"},
		{name: "directory subpath", purl: "pkg:npm/hello@1.0.0#src", members: []member{{name: "package/src/hello.txt", data: "hello\n"}}, want: treeID("100644", "hello.txt", "hello\n"), root: "package/src"},
		{name: "pub keeps root", purl: "pkg:pub/hello@1.0.0", members: []member{{name: "hello.txt", data: "hello\n"}}, want: treeID("100644", "hello.txt", "hello\n")},
		{name: "keep root", purl: "pkg:npm/hello@1.0.0", args: []string{"--keep-root"}, members: []member{{name: "package/hello.txt", data: "hello\n"}}, want: wrappedTreeID("package", treeID("100644", "hello.txt", "hello\n"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := tarball(t, tc.members...)
			out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
				return response(r, 200, "application/gzip", archive), nil
			}, append([]string{tc.purl, "--json"}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			var result purl2swhid.Result
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if result.SWHID != tc.want || result.Root != tc.root {
				t.Fatalf("result=%+v want=%s root=%s", result, tc.want, tc.root)
			}
		})
	}
}

func TestCLIHardLinks(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		members := []member{{name: "package/a", kind: tar.TypeLink, link: "./package/b"}, {name: "package/b", data: "hello\n", mode: 0755}}
		if reverse {
			members[0], members[1] = members[1], members[0]
		}
		archive := tarball(t, members...)
		out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
			return response(r, 200, "application/gzip", archive), nil
		}, "pkg:npm/hello@1.0.0#a")
		if err != nil || out != "swh:1:cnt:ce013625030ba8dba906f756967f9e9ca394464a\n" {
			t.Fatalf("stdout=%q err=%v", out, err)
		}
	}
}

func TestCLIRejectsInvalidArchive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []member
		want    string
	}{
		{"traversal", []member{{name: "../escape", data: "x"}}, "invalid archive path"},
		{"file then directory", []member{{name: "package/a", data: "x"}, {name: "package/a/b", data: "y"}}, "file/directory conflict"},
		{"directory then file", []member{{name: "package/a/b", data: "x"}, {name: "package/a", data: "y"}}, "file/directory conflict"},
		{"missing hardlink", []member{{name: "package/a", kind: tar.TypeLink, link: "missing"}}, "not a file"},
		{"cycle", []member{{name: "package/a", kind: tar.TypeLink, link: "package/b"}, {name: "package/b", kind: tar.TypeLink, link: "package/a"}}, "hard link cycle"},
		{"special", []member{{name: "package/pipe", kind: tar.TypeFifo}}, "unsupported special file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := tarball(t, tc.members...)
			out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
				return response(r, 200, "application/gzip", archive), nil
			}, "pkg:npm/hello@1.0.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) || out != "" {
				t.Fatalf("stdout=%q err=%v", out, err)
			}
		})
	}
}

func TestCLIValidation(t *testing.T) {
	for _, args := range [][]string{{}, {"bad"}, {"pkg:npm/hello@1.0.0", "--timeout=0"}, {"pkg:npm/hello@1.0.0", "extra"}, {"--unknown"}} {
		_, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
			t.Fatalf("unexpected network request %s", r.URL)
			return nil, nil
		}, args...)
		if err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
	for _, arg := range []string{"--help", "--version", "completion"} {
		out, _, err := execute(t, nil, arg)
		if err != nil || out == "" {
			t.Fatalf("%s: stdout=%q err=%v", arg, out, err)
		}
	}
}

type heritageTransport struct {
	t       *testing.T
	archive []byte
	status  int
	body    string
	posts   int
}

func (h *heritageTransport) roundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "registry.npmjs.org" {
		return response(r, 200, "application/gzip", h.archive), nil
	}
	if r.Header.Get("Accept") != "application/json" {
		h.t.Fatal("missing JSON accept")
	}
	if r.Method == http.MethodPost {
		h.posts++
		if r.URL.Path != "/api/1/origin/save/" || r.URL.Query().Get("visit_type") != "tarball" ||
			r.URL.Query().Get("origin_url") != "https://registry.npmjs.org/hello/-/hello-1.0.0.tgz" {
			h.t.Fatalf("unexpected save URL: %s", r.URL)
		}
		return response(r, 200, "application/json", []byte(`{"id":42,"origin_url":"https://registry.npmjs.org/hello/-/hello-1.0.0.tgz","visit_type":"tarball","save_request_status":"pending","save_task_status":"not created"}`)), nil
	}
	if !strings.Contains(r.URL.Path, "/resolve/swh:1:dir:") {
		h.t.Fatalf("unexpected lookup URL: %s", r.URL)
	}
	typ := "application/json"
	if strings.HasPrefix(h.body, "<") {
		typ = "text/html"
	}
	body := strings.ReplaceAll(h.body, "HASH", strings.TrimPrefix(treeID("100644", "hello.txt", "hello\n"), "swh:1:dir:"))
	return response(r, h.status, typ, []byte(body)), nil
}

func TestCLICheckAndSave(t *testing.T) {
	archive := tarball(t, member{name: "package/hello.txt", data: "hello\n"})
	for _, tc := range []struct {
		name     string
		status   int
		save     bool
		body     string
		wantErr  string
		wantSave bool
	}{
		{"known", 200, false, `{"object_id":"HASH"}`, "", false},
		{"missing", 404, false, `{}`, "", false},
		{"rate limit", 429, false, `{}`, "HTTP 429", false},
		{"server error", 503, false, `{}`, "HTTP 503", false},
		{"html challenge", 200, false, `<html>challenge</html>`, "invalid resolve response", false},
		{"wrong identifier", 200, false, `{"object_id":"wrong"}`, "invalid resolve response", false},
		{"already saved", 200, true, `{"object_id":"HASH"}`, "", false},
		{"save missing", 404, true, `{}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &heritageTransport{t: t, archive: archive, status: tc.status, body: tc.body}
			args := []string{"pkg:npm/hello@1.0.0", "--json", "--check", "--swh-url=https://swh.example"}
			if tc.save {
				args = append(args, "--save")
			}
			out, _, err := execute(t, h.roundTrip, args...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("stdout=%q err=%v", out, err)
				}
				checkPartialResult(t, out)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result purl2swhid.Result
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if result.Known == nil || *result.Known != (tc.status == 200) {
				t.Fatalf("known=%v", result.Known)
			}
			if (h.posts == 1) != tc.wantSave || (result.Save != nil) != tc.wantSave {
				t.Fatalf("posts=%d save=%+v", h.posts, result.Save)
			}
		})
	}
}

func checkPartialResult(t *testing.T, output string) {
	t.Helper()
	var result purl2swhid.Result
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.SWHID != treeID("100644", "hello.txt", "hello\n") || result.Known != nil {
		t.Fatalf("partial result=%+v", result)
	}
}

func TestCLIZipGoModule(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	header := &zip.FileHeader{Name: "github.com/example/hello@v1.0.0/hello.txt"}
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(w, "hello\n"); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://proxy.golang.org/github.com/example/hello/@v/v1.0.0.zip" {
			t.Fatalf("URL=%s", r.URL)
		}
		return response(r, 200, "application/zip", archive.Bytes()), nil
	}, "pkg:golang/github.com/example/hello@v1.0.0")
	if err != nil || out != treeID("100644", "hello.txt", "hello\n")+"\n" {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
}

func TestCLICancellation(t *testing.T) {
	cmd := newRoot(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })})
	cmd.SetArgs([]string{"pkg:npm/hello@1.0.0"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cmd.ExecuteContext(ctx); err == nil {
		t.Fatal("expected cancellation")
	}
}
