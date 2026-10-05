package docker

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// ErrForeignImage is returned when a saved image carries a name other than
// the one it was asked for.
var ErrForeignImage = errors.New("the archive names an image other than the one that was built")

// savedMetaLimit bounds the files of an archive that are read whole. They
// list an image's layers and names and are a few kilobytes.
const savedMetaLimit = 4 << 20

// A layer that equals one of another image in the same archive is a link
// to it in what older Docker versions write. No other link belongs there.
var layerLinkRE = regexp.MustCompile(`^\.\./[0-9a-f]{64}/layer\.tar$`)

// FilterSaved copies the output of `docker save` from src to dst, for
// `docker load` on another server, and refuses an archive that would give
// any image a name other than image.
//
// `docker load` tags whatever names the archive holds. The server an image
// is built on is not trusted with the server it runs on: without this, it
// could send an archive that also replaces `nginx:alpine`, or another
// app's image, with one of its own.
//
// The names are checked before the file that holds them is passed on, so a
// refused archive reaches `docker load` cut short, and it loads nothing.
func FilterSaved(dst io.Writer, src io.Reader, image string) error {
	_, tag, ok := strings.Cut(image, ":")
	if !ok || tag == "" {
		return fmt.Errorf("image %q has no tag", image)
	}
	names := map[string]bool{image: true, "docker.io/" + image: true}
	tr := tar.NewReader(src)
	tw := tar.NewWriter(dst)
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read the image archive: %w", err)
		}
		name := path.Clean(hdr.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("%w: it holds the path %q", ErrForeignImage, hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink:
			if path.Base(name) != "layer.tar" || !layerLinkRE.MatchString(hdr.Linkname) {
				return fmt.Errorf("%w: it holds a link at %q", ErrForeignImage, hdr.Name)
			}
		default:
			return fmt.Errorf("%w: it holds something that is not a file at %q", ErrForeignImage, hdr.Name)
		}
		meta := strings.ToLower(name)
		if meta != "manifest.json" && meta != "index.json" && meta != "repositories" {
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := io.Copy(tw, tr); err != nil {
				return err
			}
			continue
		}
		// One of the files that give images their names. It must be that
		// file and nothing that only resembles it or stands in for it.
		if name != meta || hdr.Typeflag != tar.TypeReg || seen[meta] {
			return fmt.Errorf("%w: %q is not what it should be", ErrForeignImage, hdr.Name)
		}
		seen[meta] = true
		if hdr.Size > savedMetaLimit {
			return fmt.Errorf("%w: %s is too large", ErrForeignImage, name)
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			return fmt.Errorf("read the image archive: %w", err)
		}
		if err := checkSavedNames(meta, raw, names, tag); err != nil {
			return err
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(raw); err != nil {
			return err
		}
	}
	if !seen["manifest.json"] && !seen["index.json"] {
		return fmt.Errorf("%w: it does not say what it holds", ErrForeignImage)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	// Whatever follows the archive's end is read and dropped, so that the
	// sending side can finish.
	_, err := io.Copy(io.Discard, src)
	return err
}

// checkSavedNames reads one of the three files through which an archive
// names images and refuses any name that is not expected.
func checkSavedNames(file string, raw []byte, names map[string]bool, tag string) error {
	foreign := func(name string) error {
		return fmt.Errorf("%w: %s names %q", ErrForeignImage, file, name)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	switch file {
	case "manifest.json":
		var entries []struct {
			RepoTags []string `json:"RepoTags"`
		}
		if err := dec.Decode(&entries); err != nil {
			return fmt.Errorf("%w: manifest.json cannot be read", ErrForeignImage)
		}
		for _, e := range entries {
			for _, name := range e.RepoTags {
				if !names[name] {
					return foreign(name)
				}
			}
		}
	case "index.json":
		var index struct {
			Manifests []struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"manifests"`
		}
		if err := dec.Decode(&index); err != nil {
			return fmt.Errorf("%w: index.json cannot be read", ErrForeignImage)
		}
		for _, m := range index.Manifests {
			for key, name := range m.Annotations {
				switch key {
				case "io.containerd.image.name":
					if !names[name] {
						return foreign(name)
					}
				case "org.opencontainers.image.ref.name":
					// The tag alone, or the whole name.
					if name != tag && !names[name] {
						return foreign(name)
					}
				}
			}
		}
	case "repositories":
		var repos map[string]map[string]string
		if err := dec.Decode(&repos); err != nil {
			return fmt.Errorf("%w: repositories cannot be read", ErrForeignImage)
		}
		for repo, tags := range repos {
			for t := range tags {
				if !names[repo+":"+t] {
					return foreign(repo + ":" + t)
				}
			}
		}
	}
	return nil
}
