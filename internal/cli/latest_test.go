package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/git-pkgs/purl2swhid"
)

func TestCLILatestVersion(t *testing.T) {
	for _, tc := range []struct {
		name, advertised, input, selected string
	}{
		{"advertised", "1.0.0", "", "1.0.0"},
		{"version ordering fallback", "", "", "2.0.0"},
		{"explicit version", "2.0.0", "@1.0.0", "1.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata, err := json.Marshal(map[string]any{
				"info": map[string]string{"name": "hello", "version": tc.advertised},
				"releases": map[string]any{
					"1.0.0": []any{map[string]string{"packagetype": "sdist", "url": "https://files.example/hello-1.0.0.tar.gz"}},
					"2.0.0": []any{map[string]string{"packagetype": "sdist", "url": "https://files.example/hello-2.0.0.tar.gz"}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			archive := tarball(t, member{name: "hello-" + tc.selected + "/hello.txt", data: "hello\n"})
			downloads := 0
			out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
				switch r.URL.String() {
				case "https://pypi.example/pypi/hello/json":
					return response(r, 200, "application/json", metadata), nil
				case "https://files.example/hello-" + tc.selected + ".tar.gz":
					downloads++
					return response(r, 200, "application/gzip", archive), nil
				default:
					t.Fatalf("unexpected request: %s", r.URL)
					return nil, nil
				}
			}, "pkg:pypi/hello"+tc.input+"?repository_url=https://pypi.example#hello.txt", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var result purl2swhid.Result
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			wantPURL := "pkg:pypi/hello@" + tc.selected + "?repository_url=https:%2F%2Fpypi.example#hello.txt"
			if result.PURL != wantPURL || result.Root != "hello-"+tc.selected+"/hello.txt" || downloads != 1 || result.SWHID != "swh:1:cnt:ce013625030ba8dba906f756967f9e9ca394464a" {
				t.Fatalf("result=%+v downloads=%d", result, downloads)
			}
		})
	}
}

func TestCLILatestDirectDownload(t *testing.T) {
	archive := tarball(t, member{name: "package/hello.txt", data: "hello\n"})
	metadata := []byte(`{"name":"hello","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"hello","version":"1.0.0"},"2.0.0":{"name":"hello","version":"2.0.0"}}}`)
	out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://registry.npmjs.org/hello":
			return response(r, 200, "application/json", metadata), nil
		case "https://registry.npmjs.org/hello/-/hello-1.0.0.tgz":
			return response(r, 200, "application/gzip", archive), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL)
			return nil, nil
		}
	}, "pkg:npm/hello", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result purl2swhid.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.PURL != "pkg:npm/hello@1.0.0" || result.SWHID != treeID("100644", "hello.txt", "hello\n") {
		t.Fatalf("result=%+v", result)
	}
}

func TestCLILatestVersionFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"empty releases", `{"info":{"name":"hello"},"releases":{}}`, "no latest version available", 200},
		{"missing package", `{}`, "resolve latest version", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := execute(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://pypi.org/pypi/hello/json" {
					t.Fatalf("unexpected request: %s", r.URL)
				}
				return response(r, tc.status, "application/json", []byte(tc.body)), nil
			}, "pkg:pypi/hello")
			if out != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("stdout=%q err=%v", out, err)
			}
		})
	}
}
