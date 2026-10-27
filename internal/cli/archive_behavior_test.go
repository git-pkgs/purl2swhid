package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/git-pkgs/purl2swhid"
)

func TestCLIRepeatedArchiveEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []member
		want    string
	}{
		{"directories", []member{{name: "package/", kind: tar.TypeDir}, {name: "package/hello.txt", data: "hello\n"}, {name: "./package/", kind: tar.TypeDir}}, treeID("100644", "hello.txt", "hello\n")},
		{"files last wins", []member{{name: "package/hello.txt", data: "old", mode: 0755}, {name: "./package/hello.txt", data: "hello\n"}}, treeID("100644", "hello.txt", "hello\n")},
		{"hardlink replaced", []member{{name: "package/hello.txt", kind: tar.TypeLink, link: "missing"}, {name: "package/hello.txt", data: "hello\n"}}, treeID("100644", "hello.txt", "hello\n")},
		{"file replaced by symlink", []member{{name: "package/hello.txt", data: "old"}, {name: "package/hello.txt", kind: tar.TypeSymlink, link: "target"}}, treeID("120000", "hello.txt", "target")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := tarball(t, tc.members...)
			out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
				return response(r, 200, "application/gzip", archive), nil
			}, "pkg:npm/hello@1.0.0")
			if err != nil || out != tc.want+"\n" {
				t.Fatalf("stdout=%q err=%v", out, err)
			}
		})
	}
}

const reviewPyPI = "pkg:pypi/hello@1.0.0?repository_url=https://pypi.example"

func pypiArtifact(t *testing.T, filename, kind string, archive []byte) transportFunc {
	t.Helper()
	metadata := map[string]any{"releases": map[string]any{"1.0.0": []any{map[string]any{
		"filename": filename, "packagetype": kind, "url": "https://registry.npmjs.org/" + filename,
	}}}}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "pypi.example" {
			return response(r, 200, "application/json", encoded), nil
		}
		return response(r, 200, "application/octet-stream", archive), nil
	}
}

func gzipContent(t *testing.T, content []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	w := gzip.NewWriter(&archive)
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestCLICompressedSingleFile(t *testing.T) {
	for _, data := range []string{"hello\n", ""} {
		t.Run(data, func(t *testing.T) {
			archive := gzipContent(t, []byte(data))
			out, _, err := execute(t, pypiArtifact(t, "hello.gz", "bdist_dumb", archive), reviewPyPI, "--filename=hello.gz")
			if err != nil || out != treeID("100644", "content", data)+"\n" {
				t.Fatalf("stdout=%q err=%v", out, err)
			}
		})
	}
}

func TestCLICompressedReadFailure(t *testing.T) {
	archive := gzipContent(t, bytes.Repeat([]byte("x"), 1024))
	archive = archive[:len(archive)-4]
	out, _, err := execute(t, pypiArtifact(t, "hello.gz", "bdist_dumb", archive), reviewPyPI, "--filename=hello.gz")
	if err == nil || out != "" {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
}

func TestResolveContentIdentifier(t *testing.T) {
	archive := tarball(t, member{name: "package/hello.txt", data: "hello\n"})
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return response(r, 200, "application/gzip", archive), nil
	})}
	result, err := purl2swhid.Resolve(context.Background(), "pkg:npm/hello@1.0.0#hello.txt", purl2swhid.Options{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	if result.SWHID != "swh:1:cnt:ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatalf("result=%+v", result)
	}
}

func wrappedZip(t *testing.T) []byte {
	t.Helper()
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	header := &zip.FileHeader{Name: "hello-1.0.0/hello.txt"}
	header.SetMode(0644)
	file, err := w.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "hello\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestCLIPyPIWrapperPolicy(t *testing.T) {
	zipData := wrappedZip(t)
	tarData := tarball(t, member{name: "hello-1.0.0/hello.txt", data: "hello\n"})
	for _, tc := range []struct {
		filename, kind string
		archive        []byte
		strip          bool
	}{
		{"hello-1.0.0.tar.gz", "sdist", tarData, true},
		{"hello-1.0.0.zip", "sdist", zipData, true},
		{"hello-1.0.0.zip", "bdist_dumb", zipData, false},
		{"hello-1.0.0.egg", "bdist_egg", zipData, false},
		{"hello-1.0.0.whl", "bdist_wheel", zipData, false},
		{"hello-1.0.0.tar.gz", "bdist_dumb", tarData, false},
	} {
		t.Run(tc.kind+tc.filename, func(t *testing.T) {
			out, _, err := execute(t, pypiArtifact(t, tc.filename, tc.kind, tc.archive), reviewPyPI, "--filename="+tc.filename, "--json")
			if err != nil {
				t.Fatal(err)
			}
			var result purl2swhid.Result
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			want := treeID("100644", "hello.txt", "hello\n")
			root := "hello-1.0.0"
			if !tc.strip {
				want = wrappedTreeID(root, want)
				root = ""
			}
			if result.SWHID != want || result.Root != root {
				t.Fatalf("result=%+v want=%s root=%q", result, want, root)
			}
		})
	}
}
