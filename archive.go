package purl2swhid

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/andrew/swhid-go"
	"github.com/andrew/swhid-go/objects"
	"github.com/git-pkgs/archives"
)

const (
	maxDepth      = 128
	maxEntryBytes = maxArchiveBytes
)

type node struct {
	children map[string]*node
	entry    objects.DirectoryEntry
	link     string
}

func readArchive(ctx context.Context, filename string, body io.Reader, goModule bool) (_ *node, err error) {
	stream, err := archives.OpenStreamContext(ctx, filename, body, archives.StreamOptions{MaxEntryBytes: maxEntryBytes})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	root := &node{children: make(map[string]*node)}
	entries := make(map[string]*node)
	for {
		entry, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := cleanPath(entry.Path)
		if entry.IsDir && (name == "." || name == "") {
			continue
		}
		child, err := root.insert(name, entry.IsDir)
		if err != nil {
			return nil, err
		}
		entries[name] = child
		if goModule && !entry.IsDir && fs.FileMode(entry.Mode).IsRegular() {
			// Go module ZIPs omit file modes; module files are non-executable.
			entry.Mode, entry.HasMode = 0644, true
		}
		if err := child.read(entry, stream); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, child := range entries {
		if err := child.resolveLink(entries, 0); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return root, nil
}

func cleanPath(name string) string {
	for strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	return strings.TrimSuffix(name, "/")
}

func (n *node) insert(name string, directory bool) (*node, error) {
	if !fs.ValidPath(name) || name == "." || strings.ContainsRune(name, 0) {
		return nil, fmt.Errorf("invalid archive path %q", name)
	}
	parts := strings.Split(name, "/")
	if len(parts) > maxDepth {
		return nil, fmt.Errorf("archive path exceeds %d components", maxDepth)
	}
	for i, part := range parts {
		if n.children == nil {
			return nil, fmt.Errorf("file/directory conflict at %q", name)
		}
		isDir := i < len(parts)-1 || directory
		child := n.children[part]
		if child == nil {
			child = &node{}
			if isDir {
				child.children = make(map[string]*node)
			}
			n.children[part] = child
		} else if isDir != (child.children != nil) {
			return nil, fmt.Errorf("file/directory conflict at %q", name)
		}
		n = child
	}
	return n, nil
}

func (n *node) read(entry *archives.StreamEntry, body io.Reader) error {
	if entry.IsDir {
		return nil
	}
	n.entry = objects.DirectoryEntry{}
	n.link = ""
	if entry.IsHardlink {
		n.link = cleanPath(entry.Linkname)
		if n.link == "" {
			return errors.New("empty hard link target")
		}
		return nil
	}
	// A compressed single-file stream has no mode metadata or declared size.
	if !entry.HasMode && entry.Size != -1 {
		return errors.New("archive does not record Unix file modes")
	}
	mode := fs.FileMode(entry.Mode)
	var id *swhid.Identifier
	var err error
	switch mode.Type() {
	case 0:
		n.entry.Type = objects.EntryTypeFile
		if mode.Perm()&0111 != 0 {
			n.entry.Type = objects.EntryTypeExecutable
		}
	case fs.ModeSymlink:
		n.entry.Type = objects.EntryTypeSymlink
	default:
		return errors.New("unsupported special file")
	}
	switch {
	case n.entry.Type == objects.EntryTypeSymlink && entry.Linkname != "":
		id, err = swhid.FromContent([]byte(entry.Linkname))
	case entry.Size == -1:
		var data []byte
		data, err = io.ReadAll(io.LimitReader(body, maxEntryBytes+1))
		if err == nil && len(data) > maxEntryBytes {
			err = archives.ErrEntrySizeLimit
		}
		if err == nil {
			id, err = swhid.FromContent(data)
		}
	default:
		id, err = swhid.FromContentReader(body, entry.Size)
	}
	if err != nil {
		return err
	}
	n.entry.Target = id.ObjectHash
	return nil
}

func (n *node) resolveLink(entries map[string]*node, depth int) error {
	if n.link == "" {
		return nil
	}
	if depth >= maxDepth {
		return errors.New("hard link cycle or chain too deep")
	}
	target := entries[n.link]
	if target == nil || target.children != nil {
		return fmt.Errorf("hard link target %q is not a file", n.link)
	}
	if err := target.resolveLink(entries, depth+1); err != nil {
		return err
	}
	n.entry = target.entry
	n.link = ""
	return nil
}

func (n *node) identifier() (*swhid.Identifier, error) {
	if n.children == nil {
		return swhid.NewIdentifier(swhid.ObjectTypeContent, n.entry.Target, nil)
	}
	entries := make([]objects.DirectoryEntry, 0, len(n.children))
	for name, child := range n.children {
		entry := child.entry
		entry.Name = name
		if child.children != nil {
			id, err := child.identifier()
			if err != nil {
				return nil, err
			}
			entry.Type, entry.Target = objects.EntryTypeDirectory, id.ObjectHash
		}
		entries = append(entries, entry)
	}
	return swhid.FromDirectory(entries)
}
