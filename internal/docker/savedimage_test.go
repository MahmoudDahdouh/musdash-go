package docker

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type entry struct {
	name, body, link string
	kind             byte
}

func archive(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg, Linkname: e.link}
		if e.kind != 0 {
			hdr.Typeflag, hdr.Size = e.kind, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	return &buf
}

const (
	built = "musdash/a7g2eccc434f:0123456789ab"
	layer = "0000000000000000000000000000000000000000000000000000000000000001"
)

func manifest(tags ...string) entry {
	return entry{name: "manifest.json", body: `[{"Config":"c.json","RepoTags":["` + strings.Join(tags, `","`) + `"],"Layers":["` + layer + `/layer.tar"]}]`}
}

func TestFilterSavedPassesAnArchiveOfTheBuiltImage(t *testing.T) {
	in := archive(t,
		entry{name: "blobs/", kind: tar.TypeDir},
		entry{name: "blobs/sha256/aa", body: strings.Repeat("layer", 5000)},
		entry{name: layer + "/layer.tar", body: "x"},
		entry{name: "0000000000000000000000000000000000000000000000000000000000000002/layer.tar", kind: tar.TypeSymlink, link: "../" + layer + "/layer.tar"},
		entry{name: "index.json", body: `{"schemaVersion":2,"manifests":[{"digest":"sha256:aa","annotations":{"io.containerd.image.name":"docker.io/` + built + `","org.opencontainers.image.ref.name":"0123456789ab"}}]}`},
		manifest(built),
		entry{name: "repositories", body: `{"musdash/a7g2eccc434f":{"0123456789ab":"` + layer + `"}}`},
		entry{name: "oci-layout", body: `{"imageLayoutVersion":"1.0.0"}`},
	)
	want := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(in.Bytes()))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		body, _ := io.ReadAll(tr)
		want[hdr.Name] = string(hdr.Typeflag) + hdr.Linkname + string(body)
	}

	var out bytes.Buffer
	if err := FilterSaved(&out, in, built); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	tr = tar.NewReader(&out)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		got[hdr.Name] = string(hdr.Typeflag) + hdr.Linkname + string(body)
	}
	if len(got) != len(want) {
		t.Fatalf("%d entries came out, %d went in", len(got), len(want))
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s changed on the way", name)
		}
	}
}

func TestFilterSavedRefusesOtherNames(t *testing.T) {
	blob := entry{name: "blobs/sha256/aa", body: "layer"}
	cases := map[string][]entry{
		"another image in the manifest":  {blob, manifest(built, "nginx:alpine")},
		"another app's image":            {blob, manifest("musdash/otherapp00000:0123456789ab")},
		"the same repository, other tag": {blob, manifest("musdash/a7g2eccc434f:latest")},
		"a second manifest entry":        {blob, {name: "manifest.json", body: `[{"RepoTags":["` + built + `"]},{"RepoTags":["nginx:alpine"]}]`}},
		"a name in index.json": {blob, manifest(built),
			{name: "index.json", body: `{"manifests":[{"annotations":{"io.containerd.image.name":"docker.io/library/nginx:alpine"}}]}`}},
		"a ref name in index.json": {blob, manifest(built),
			{name: "index.json", body: `{"manifests":[{"annotations":{"org.opencontainers.image.ref.name":"nginx:alpine"}}]}`}},
		"a name in repositories":        {blob, manifest(built), {name: "repositories", body: `{"nginx":{"alpine":"x"}}`}},
		"a manifest given twice":        {manifest(built), blob, manifest("nginx:alpine")},
		"a manifest under ./":           {manifest(built), {name: "./manifest.json", body: `[{"RepoTags":["nginx:alpine"]}]`}},
		"a manifest in other case":      {manifest(built), {name: "Manifest.json", body: `[{"RepoTags":["nginx:alpine"]}]`}},
		"a manifest that is a link":     {blob, {name: "manifest.json", kind: tar.TypeSymlink, link: "blobs/sha256/aa"}},
		"a manifest that is a hardlink": {blob, {name: "manifest.json", kind: tar.TypeLink, link: "blobs/sha256/aa"}},
		"a link elsewhere":              {blob, manifest(built), {name: "x/layer.tar", kind: tar.TypeSymlink, link: "/etc/passwd"}},
		"a link that is not a layer":    {blob, manifest(built), {name: "x/other", kind: tar.TypeSymlink, link: "../" + layer + "/layer.tar"}},
		"a path that climbs out":        {blob, manifest(built), {name: "../../etc/cron.d/x", body: "x"}},
		"an absolute path":              {blob, manifest(built), {name: "/etc/cron.d/x", body: "x"}},
		"a device":                      {blob, manifest(built), {name: "dev", kind: tar.TypeChar}},
		"a manifest that is not JSON":   {blob, {name: "manifest.json", body: `nginx:alpine`}},
		"nothing that names the image":  {blob},
	}
	for name, entries := range cases {
		var out bytes.Buffer
		err := FilterSaved(&out, archive(t, entries...), built)
		if !errors.Is(err, ErrForeignImage) {
			t.Errorf("%s: %v, want ErrForeignImage", name, err)
			continue
		}
		// What was refused did not go through: the receiving side never
		// sees the file with the unwanted name.
		tr := tar.NewReader(&out)
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			body, _ := io.ReadAll(tr)
			if strings.Contains(string(body), "nginx") || strings.Contains(string(body), "otherapp") || strings.Contains(hdr.Name, "etc") {
				t.Errorf("%s: %s was passed on before the archive was refused", name, hdr.Name)
			}
		}
	}
}

func TestFilterSavedRefusesACutOffArchive(t *testing.T) {
	in := archive(t, entry{name: "blobs/sha256/aa", body: strings.Repeat("x", 4000)}, manifest(built))
	cut := bytes.NewReader(in.Bytes()[:1500])
	if err := FilterSaved(io.Discard, cut, built); err == nil {
		t.Fatal("half an archive was taken for a whole one")
	}
	if err := FilterSaved(io.Discard, strings.NewReader(""), built); err == nil {
		t.Fatal("an empty stream was taken for an image")
	}
	if err := FilterSaved(io.Discard, archive(t, manifest("x")), "no-tag"); err == nil {
		t.Fatal("an image without a tag was accepted")
	}
}

// With the archives a real Docker writes. Needs an image that is already
// on this machine; it is only given a second name, which is removed again.
func TestFilterSavedWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against Docker")
	}
	base := ""
	for _, candidate := range []string{"alpine:latest", "alpine:3", "nginx:alpine", "busybox:latest"} {
		if exec.Command("docker", "image", "inspect", candidate).Run() == nil {
			base = candidate
			break
		}
	}
	if base == "" {
		t.Skip("no small image on this machine to try with")
	}
	const mine = "musdash/filtertest00:0123456789ab"
	if out, err := exec.Command("docker", "tag", base, mine).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	defer exec.Command("docker", "rmi", mine).Run()

	move := func(saved ...string) (string, error) {
		save := exec.Command("docker", append([]string{"save", "--"}, saved...)...)
		load := exec.Command("docker", "load")
		src, _ := save.StdoutPipe()
		dst, _ := load.StdinPipe()
		var said bytes.Buffer
		load.Stdout, load.Stderr = &said, &said
		if err := save.Start(); err != nil {
			t.Fatal(err)
		}
		if err := load.Start(); err != nil {
			t.Fatal(err)
		}
		ferr := FilterSaved(dst, src, mine)
		if ferr != nil {
			// Cut short, as the pipe between two servers is.
			io.Copy(io.Discard, src)
		}
		dst.Close()
		save.Wait()
		lerr := load.Wait()
		if ferr == nil {
			ferr = lerr
		}
		return said.String(), ferr
	}
	said, err := move(mine)
	if err != nil || !strings.Contains(said, mine) {
		t.Fatalf("an archive of the image alone: %v\n%s", err, said)
	}
	said, err = move(mine, base)
	if !errors.Is(err, ErrForeignImage) {
		t.Fatalf("an archive with a second image: %v\n%s", err, said)
	}
	if strings.Contains(said, "Loaded image") {
		t.Fatalf("Docker loaded something from a refused archive:\n%s", said)
	}
}
