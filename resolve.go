// Package purl2swhid computes SWHIDs for published package contents.
package purl2swhid

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/git-pkgs/purl"
	"github.com/git-pkgs/registries"
	_ "github.com/git-pkgs/registries/all"
	"github.com/git-pkgs/registries/fetch"
)

const maxArchiveBytes = 512 << 20

type Options struct {
	Filename   string
	KeepRoot   bool
	Check      bool
	Save       bool
	SWHURL     string
	Token      string
	HTTPClient *http.Client
}

type Result struct {
	PURL       string       `json:"purl"`
	SWHID      string       `json:"swhid"`
	ArchiveURL string       `json:"archive_url"`
	Filename   string       `json:"filename"`
	Root       string       `json:"root,omitempty"`
	Known      *bool        `json:"known,omitempty"`
	Save       *SaveRequest `json:"save,omitempty"`
}

// Resolve retains the computed result when a preservation request fails.
func Resolve(ctx context.Context, raw string, opts Options) (_ *Result, err error) {
	p, err := purl.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse package URL: %w", err)
	}
	info, err := resolveArtifact(ctx, p, opts)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact: %w", err)
	}
	fetchOptions := []fetch.Option{fetch.WithMaxArtifactSize(maxArchiveBytes), fetch.WithUserAgent("purl2swhid")}
	if opts.HTTPClient != nil {
		fetchOptions = append(fetchOptions, fetch.WithHTTPClient(opts.HTTPClient))
	}
	f := fetch.NewFetcher(fetchOptions...)
	defer func() { err = errors.Join(err, f.Close()) }()
	artifact, err := f.Fetch(ctx, info.URL)
	if err != nil {
		return nil, fmt.Errorf("download archive: %w", err)
	}
	defer func() { err = errors.Join(err, artifact.Body.Close()) }()
	tree, err := readArchive(ctx, info.Filename, artifact.Body, p.Type == "golang")
	if err != nil {
		return nil, fmt.Errorf("hash archive: %w", err)
	}
	// Read the remaining response to detect truncated transfers and size overruns.
	if _, err := io.Copy(io.Discard, artifact.Body); err != nil {
		return nil, fmt.Errorf("finish download: %w", err)
	}
	root, prefix, err := packageRoot(tree, p, info.sourceDistribution, opts.KeepRoot)
	if err != nil {
		return nil, err
	}
	id, err := root.identifier()
	if err != nil {
		return nil, err
	}
	result := &Result{PURL: p.String(), SWHID: id.String(), ArchiveURL: info.URL, Filename: info.Filename, Root: prefix}
	if opts.Check || opts.Save {
		if err := checkArchive(ctx, result, opts); err != nil {
			return result, err
		}
	}
	return result, nil
}

type resolvedArtifact struct {
	*fetch.ArtifactInfo
	sourceDistribution bool
}

func resolveArtifact(ctx context.Context, p *purl.PURL, opts Options) (*resolvedArtifact, error) {
	var clientOptions []registries.Option
	if opts.HTTPClient != nil {
		clientOptions = append(clientOptions, registries.WithHTTPClient(opts.HTTPClient))
	}
	client := registries.NewClient(clientOptions...)
	reg, name, version, err := registries.NewFromPURL(p.String(), client)
	if err != nil {
		return nil, err
	}
	if version == "" {
		latest, err := registries.FetchLatestVersion(ctx, reg, name)
		if err != nil {
			return nil, fmt.Errorf("resolve latest version: %w", err)
		}
		if latest == nil || latest.Number == "" {
			return nil, errors.New("no latest version available")
		}
		version = latest.Number
		p.Version = version
	}
	resolver := fetch.NewResolver()
	resolver.RegisterRegistry(reg)
	var release *registries.Version
	if reg.URLs().Download(name, version) == "" {
		release, err = registries.FetchVersionFromPURL(ctx, p.String(), client)
		if err != nil {
			return nil, err
		}
		resolver.RegisterRegistry(&releaseRegistry{Registry: reg, release: release})
		if p.Type == "pypi" && opts.Filename == "" {
			opts.Filename, err = sourceFilename(release)
			if err != nil {
				return nil, err
			}
		}
	}
	info, err := resolver.ResolveWithOptions(ctx, p.Type, name, version, fetch.ResolveOptions{
		Filename: opts.Filename, Qualifiers: p.Qualifiers.Map(),
	})
	if err != nil {
		return nil, err
	}
	result := &resolvedArtifact{ArtifactInfo: info}
	if release != nil {
		for _, artifact := range release.Artifacts {
			if artifact.URL == info.URL {
				result.sourceDistribution = artifact.Metadata["packagetype"] == "sdist"
				break
			}
		}
	}
	return result, nil
}

type releaseRegistry struct {
	registries.Registry
	release *registries.Version
}

func (r *releaseRegistry) FetchVersions(context.Context, string) ([]registries.Version, error) {
	return []registries.Version{*r.release}, nil
}

func sourceFilename(release *registries.Version) (string, error) {
	for _, artifact := range release.Artifacts {
		if artifact.Metadata["packagetype"] == "sdist" && artifact.Filename != "" {
			return artifact.Filename, nil
		}
	}
	return "", errors.New("no PyPI source distribution; use --filename to select an artifact")
}

func packageRoot(root *node, p *purl.PURL, sourceDistribution, keep bool) (*node, string, error) {
	prefix := ""
	if !keep {
		switch p.Type {
		case "npm", "cargo":
			prefix = wrapper(root)
		case "pypi":
			if sourceDistribution {
				prefix = wrapper(root)
			}
		case "golang":
			prefix = p.FullName() + "@" + p.Version
		}
	}
	selected := strings.Trim(strings.Join([]string{prefix, p.Subpath}, "/"), "/")
	if selected != "" {
		for _, part := range strings.Split(selected, "/") {
			root = root.children[part]
			if root == nil {
				return nil, "", fmt.Errorf("package path %q does not exist in archive", selected)
			}
		}
	}
	return root, selected, nil
}

func wrapper(root *node) string {
	if len(root.children) == 1 {
		for name, child := range root.children {
			if child.children != nil {
				return name
			}
		}
	}
	return ""
}
