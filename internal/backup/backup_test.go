package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
)

func gunzip(t *testing.T, raw string) string {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestDumpWritesACompressedFile(t *testing.T) {
	fake := &runnertest.Fake{Handle: func(line string, _ runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker exec musdash-db-x sh -c pg_dump") {
			return strings.Repeat("INSERT INTO t VALUES (1);\n", 1000), nil
		}
		return "", nil
	}}
	size, err := Dump(context.Background(), fake, "musdash-db-x", `pg_dump -U "$POSTGRES_USER"`, "/backups/x/one.gz")
	if err != nil {
		t.Fatal(err)
	}
	raw, mode, ok := fake.File("/backups/x/one.gz")
	if !ok || mode != 0o600 {
		t.Fatalf("file missing or not private (mode %o)", mode)
	}
	if int64(len(raw)) != size || size >= 26000 {
		t.Fatalf("size %d reported, %d written; 26000 bytes should have been compressed", size, len(raw))
	}
	if got := gunzip(t, raw); strings.Count(got, "INSERT") != 1000 {
		t.Fatalf("the file does not hold the dump: %d bytes", len(got))
	}
}

func TestAFailedDumpLeavesNoFile(t *testing.T) {
	// The tool writes part of a dump, then fails: a short file must not be
	// kept as a backup.
	fake := &runnertest.Fake{Handle: func(line string, c runner.Cmd) (string, error) {
		if strings.HasPrefix(line, "docker exec") {
			if c.Stderr != nil {
				io.WriteString(c.Stderr, "pg_dump: error: connection to server failed\n")
			}
			return "CREATE TABLE half (", runnertest.Exit("docker", 1, "")
		}
		return "", nil
	}}
	size, err := Dump(context.Background(), fake, "musdash-db-x", "pg_dump", "/backups/x/one.gz")
	if err == nil || size != 0 || !strings.Contains(err.Error(), "connection to server failed") {
		t.Fatalf("a dump that failed halfway: size %d, %v", size, err)
	}
	if _, _, ok := fake.File("/backups/x/one.gz"); ok {
		t.Fatal("a partial dump was kept under the backup's name")
	}

	// The disk refuses the file.
	fake = &runnertest.Fake{
		Handle:    func(string, runner.Cmd) (string, error) { return "data", nil },
		FailWrite: func(string) error { return os.ErrPermission },
	}
	if _, err := Dump(context.Background(), fake, "musdash-db-x", "pg_dump", "/backups/x/one.gz"); err == nil || !strings.Contains(err.Error(), "write the backup file") {
		t.Fatalf("a failed write: %v", err)
	}

	for name, args := range map[string][2]string{
		"no command":       {"musdash-db-x", ""},
		"container option": {"--privileged", "pg_dump"},
	} {
		if _, err := Dump(context.Background(), fake, args[0], args[1], "/b/one.gz"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRestoreFeedsTheFileBack(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	io.WriteString(zw, "CREATE TABLE t (v text);\n")
	zw.Close()
	var fed string
	fake := &runnertest.Fake{Handle: func(line string, c runner.Cmd) (string, error) {
		if line == "docker exec --interactive musdash-db-x sh -c psql" && c.Stdin != nil {
			raw, _ := io.ReadAll(c.Stdin)
			fed = string(raw)
		}
		return "", nil
	}}
	fake.PutFile("/backups/x/one.gz", buf.String())
	if err := Restore(context.Background(), fake, "musdash-db-x", "psql", "/backups/x/one.gz"); err != nil {
		t.Fatal(err)
	}
	if fed != "CREATE TABLE t (v text);\n" {
		t.Fatalf("the restore command was fed %q", fed)
	}

	if err := Restore(context.Background(), fake, "musdash-db-x", "psql", "/backups/x/missing.gz"); err == nil {
		t.Fatal("a missing file was restored")
	}
	fake.PutFile("/backups/x/plain.gz", "not gzip at all")
	if err := Restore(context.Background(), fake, "musdash-db-x", "psql", "/backups/x/plain.gz"); err == nil || !strings.Contains(err.Error(), "not a gzip file") {
		t.Fatalf("a file that is not gzip: %v", err)
	}
	fake.Handle = func(_ string, c runner.Cmd) (string, error) {
		if c.Stdin != nil {
			io.Copy(io.Discard, c.Stdin)
		}
		return "ERROR:  relation \"t\" already exists\n", runnertest.Exit("docker", 3, "")
	}
	if err := Restore(context.Background(), fake, "musdash-db-x", "psql", "/backups/x/one.gz"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a failing restore: %v", err)
	}
}

var testS3 = S3{Endpoint: "https://s3.example.com", Region: "eu-central-1", Bucket: "my-backups", Prefix: "musdash/prod", AccessKey: "AKIAEXAMPLE", SecretKey: "s3cr3t/Key+Value=",
	Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("2001:db8::7"), netip.MustParseAddr("198.51.100.7")}, nil
	}}

// envFileOf is the keys file a rclone command line names.
func envFileOf(line string) string {
	f := strings.Fields(line)
	for i, a := range f {
		if a == "--env-file" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

// keysLeft reports whether any keys file is still on the fake server.
func keysLeft(fake *runnertest.Fake) bool {
	for _, c := range fake.Calls() {
		if p := envFileOf(c); p != "" {
			if _, _, ok := fake.File(p); ok {
				return true
			}
		}
	}
	return false
}

func TestS3Commands(t *testing.T) {
	var envDuring string
	fake := &runnertest.Fake{}
	fake.Handle = func(line string, _ runner.Cmd) (string, error) {
		envDuring, _, _ = fake.File(envFileOf(line))
		if strings.Contains(line, " lsjson ") {
			return `[{"Path":"b.gz","Name":"b.gz","Size":20},{"Path":"a.gz","Name":"a.gz","Size":10}]`, nil
		}
		return "", nil
	}
	ctx := context.Background()
	if err := testS3.Upload(ctx, fake, "/backups/x", "maindb-20260310-030000.sql.gz"); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	// The name is looked up here and given to rclone already resolved, to
	// the IPv4 address when there is one.
	keys := envFileOf(calls[0])
	want := "docker run --rm --env-file " + keys + " --add-host s3.example.com:198.51.100.7 --mount type=bind,source=/backups/x,target=/backup,readonly " +
		RcloneImage + " copyto /backup/maindb-20260310-030000.sql.gz s3:my-backups/musdash/prod/maindb-20260310-030000.sql.gz --retries 2 --low-level-retries 3 --contimeout 15s --timeout 60s"
	if calls[0] != want || !strings.HasPrefix(keys, "/backups/x/"+EnvFilePrefix) {
		t.Fatalf("upload command:\n %s\nwant\n %s", calls[0], want)
	}
	// The keys are in the env file while the command runs, in no argument,
	// and the file is gone afterwards.
	for _, c := range calls {
		if strings.Contains(c, testS3.SecretKey) || strings.Contains(c, testS3.AccessKey) {
			t.Fatalf("a key is on a command line: %s", c)
		}
	}
	for _, line := range []string{
		"RCLONE_CONFIG_S3_ACCESS_KEY_ID=AKIAEXAMPLE\n", "RCLONE_CONFIG_S3_SECRET_ACCESS_KEY=s3cr3t/Key+Value=\n",
		"RCLONE_CONFIG_S3_ENDPOINT=https://s3.example.com\n", "RCLONE_CONFIG_S3_REGION=eu-central-1\n", "RCLONE_CONFIG_S3_TYPE=s3\n", "RCLONE_S3_NO_CHECK_BUCKET=true\n",
	} {
		if !strings.Contains(envDuring, line) {
			t.Errorf("the env file is missing %q:\n%s", line, envDuring)
		}
	}
	if keysLeft(fake) {
		t.Fatal("the storage keys were left on disk")
	}

	objects, err := testS3.List(ctx, fake, "/backups/x")
	if err != nil || len(objects) != 2 || objects[0] != (Object{Name: "a.gz", Size: 10}) {
		t.Fatalf("list: %+v %v", objects, err)
	}
	if err := testS3.Delete(ctx, fake, "/backups/x", "a.gz"); err != nil {
		t.Fatal(err)
	}
	last := fake.Calls()[len(fake.Calls())-2] // the last is the env file's removal
	if !strings.Contains(last, RcloneImage+" deletefile s3:my-backups/musdash/prod/a.gz ") || strings.Contains(last, "--mount") {
		t.Fatalf("delete command: %s", last)
	}

	// rclone's complaint is passed on, and the keys are still cleaned up.
	fake.Handle = func(string, runner.Cmd) (string, error) {
		return "", runnertest.Exit("docker", 1, "2026/03/10 NOTICE: retrying\nFailed to copy: AccessDenied: Access Denied\n\tstatus code: 403")
	}
	err = testS3.Upload(ctx, fake, "/backups/x", "a.gz")
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") || strings.Contains(err.Error(), testS3.SecretKey) {
		t.Fatalf("a refused upload: %v", err)
	}
	if keysLeft(fake) {
		t.Fatal("the storage keys were left on disk after a failure")
	}
	// A file that is already gone is not a failed delete.
	fake.Handle = func(string, runner.Cmd) (string, error) {
		return "", runnertest.Exit("docker", 4, "Failed to deletefile: object not found")
	}
	if err := testS3.Delete(ctx, fake, "/backups/x", "gone.gz"); err != nil {
		t.Fatalf("deleting a missing object: %v", err)
	}
}

func TestS3Validate(t *testing.T) {
	if err := testS3.Validate(); err != nil {
		t.Fatal(err)
	}
	aws := testS3
	aws.Endpoint, aws.Prefix = "", ""
	if err := aws.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(aws.envFile(), "RCLONE_CONFIG_S3_PROVIDER=AWS\n") || aws.remote("a.gz") != "s3:my-backups/a.gz" {
		t.Fatalf("AWS defaults: %s %s", aws.envFile(), aws.remote("a.gz"))
	}
	for name, change := range map[string]func(*S3){
		"endpoint with a path":      func(s *S3) { s.Endpoint = "https://s3.example.com/bucket" },
		"endpoint with credentials": func(s *S3) { s.Endpoint = "https://user:pw@s3.example.com" },
		"endpoint not http":         func(s *S3) { s.Endpoint = "file:///etc" },
		"region with a space":       func(s *S3) { s.Region = "eu central" },
		"bucket in capitals":        func(s *S3) { s.Bucket = "MyBucket" },
		"bucket as an option":       func(s *S3) { s.Bucket = "--config=/etc/passwd" },
		"bucket with a slash":       func(s *S3) { s.Bucket = "a/../../b" },
		"prefix climbing out":       func(s *S3) { s.Prefix = "../other" },
		"prefix with a space":       func(s *S3) { s.Prefix = "my folder" },
		"key with a line break":     func(s *S3) { s.SecretKey = "a\nRCLONE_CONFIG_S3_ENDPOINT=http://evil" },
		"no access key":             func(s *S3) { s.AccessKey = "" },
	} {
		s := testS3
		change(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	fake := &runnertest.Fake{}
	for _, name := range []string{"", "../x.gz", "a/b.gz", "-rf", "a b.gz", "x.gz\n"} {
		if err := testS3.Upload(context.Background(), fake, "/backups/x", name); err == nil {
			t.Errorf("object name %q: accepted", name)
		}
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("commands ran for refused names: %v", fake.Calls())
	}
}

// TestBackupWithDocker dumps a real PostgreSQL, changes it, restores the
// dump and finds the old content; then uploads the file to an S3 server,
// lists it and deletes it.
func TestBackupWithDocker(t *testing.T) {
	if os.Getenv("MUSDASH_DOCKER_TEST") != "1" {
		t.Skip("set MUSDASH_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	ctx := context.Background()
	r := runner.NewLocal()
	docker := func(args ...string) (string, error) {
		out, err := r.Output(ctx, runner.Cmd{Name: "docker", Args: args})
		return strings.TrimSpace(string(out)), err
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	pg := "musdash-test-backup-" + suffix
	s3 := "musdash-test-s3-" + suffix
	t.Cleanup(func() {
		docker("rm", "--force", "--volumes", pg, s3)
	})

	tpl, _ := catalog.Database("postgres")
	if _, err := docker("run", "--detach", "--name", pg, "--env", "POSTGRES_USER=postgres", "--env", "POSTGRES_PASSWORD=test-only", "--env", "POSTGRES_DB=postgres", tpl.Image); err != nil {
		t.Fatal(err)
	}
	ready := false
	for range 60 {
		if _, err := docker("exec", pg, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"); err == nil {
			ready = true
			break
		}
		time.Sleep(time.Second)
	}
	if !ready {
		t.Fatal("PostgreSQL did not start")
	}
	sql := func(q string) string {
		t.Helper()
		out, err := docker("exec", pg, "psql", "-U", "postgres", "-Atc", q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	sql("create table notes (v text); insert into notes values ('before the backup')")

	file := "postgres-test.dump.gz"
	size, err := Dump(ctx, r, pg, tpl.DumpCmd, filepath.Join(dir, file))
	if err != nil || size < 100 {
		t.Fatalf("dump: %d bytes, %v", size, err)
	}
	if info, err := os.Stat(filepath.Join(dir, file)); err != nil || info.Size() != size || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", info, err)
	}

	// A dump of a database that is not there fails and leaves no file.
	if _, err := Dump(ctx, r, pg, `pg_dump -U postgres no_such_database`, filepath.Join(dir, "bad.gz")); err == nil || !strings.Contains(err.Error(), "no_such_database") {
		t.Fatalf("a failing dump: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.gz")); err == nil {
		t.Fatal("a failed dump left a file")
	}

	sql("update notes set v = 'changed after the backup'; create table extra (n int)")
	if err := Restore(ctx, r, pg, tpl.RestoreCmd, filepath.Join(dir, file)); err != nil {
		t.Fatal(err)
	}
	if got := sql("select v from notes"); got != "before the backup" {
		t.Fatalf("after the restore the table holds %q", got)
	}

	// An S3 server: rclone itself can be one.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if _, err := docker("run", "--detach", "--name", s3, "--publish", "127.0.0.1:"+strconv.Itoa(port)+":8080", RcloneImage,
		"serve", "s3", "--addr", ":8080", "--auth-key", "test-access,test-secret", "/data"); err != nil {
		t.Fatal(err)
	}
	if _, err := docker("exec", s3, "mkdir", "-p", "/data/backups"); err != nil {
		t.Fatal(err)
	}
	store := S3{Endpoint: "http://host.docker.internal:" + strconv.Itoa(port), Region: "us-east-1", Bucket: "backups", Prefix: "musdash", AccessKey: "test-access", SecretKey: "test-secret", AllowLocal: true}
	var checkErr error
	for range 30 {
		if checkErr = store.Check(ctx, r, dir); checkErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if checkErr != nil {
		t.Fatalf("the storage check fails against a working server: %v", checkErr)
	}
	wrong := store
	wrong.SecretKey = "not-the-secret"
	if err := wrong.Check(ctx, r, dir); err == nil {
		t.Fatal("a wrong key passed the storage check")
	}

	if err := store.Upload(ctx, r, dir, file); err != nil {
		t.Fatal(err)
	}
	objects, err := store.List(ctx, r, dir)
	if err != nil || len(objects) != 1 || objects[0].Name != file || objects[0].Size != size {
		t.Fatalf("after the upload the storage holds %+v (%v), want one object of %d bytes", objects, err, size)
	}
	if out, _ := docker("exec", s3, "ls", "/data/backups/musdash"); out != file {
		t.Fatalf("on the S3 server's disk: %q", out)
	}
	if err := store.Delete(ctx, r, dir, file); err != nil {
		t.Fatal(err)
	}
	if objects, _ := store.List(ctx, r, dir); len(objects) != 0 {
		t.Fatalf("after the delete: %+v", objects)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, EnvFilePrefix+"*")); len(left) != 0 {
		t.Fatalf("the storage keys were left in the backup directory: %v", left)
	}
	t.Logf("PostgreSQL dumped (%d bytes), restored, uploaded to S3, listed and deleted", size)
}

// An endpoint is typed by a team member: it may not lead to the server
// itself, a container, or a metadata service, by address or by name.
func TestS3EndpointMustNotLeadInside(t *testing.T) {
	ctx := context.Background()
	fake := &runnertest.Fake{}
	base := S3{Bucket: "backups", AccessKey: "k", SecretKey: "s"}
	for _, endpoint := range []string{"http://169.254.169.254", "http://127.0.0.1:9000", "https://[::1]", "http://100.100.100.200", "http://0.0.0.0:9000"} {
		s := base
		s.Endpoint = endpoint
		if _, err := s.List(ctx, fake, "/work"); err == nil || !strings.Contains(err.Error(), "cannot be used") {
			t.Errorf("%s: %v", endpoint, err)
		}
	}
	// A public-looking name that resolves inside, even among good answers.
	s := base
	s.Endpoint = "https://storage.example.com"
	s.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("169.254.169.254")}, nil
	}
	if _, err := s.List(ctx, fake, "/work"); err == nil || !strings.Contains(err.Error(), "cannot be used") {
		t.Errorf("a name that resolves to the metadata address: %v", err)
	}
	s.Lookup = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") }
	if _, err := s.List(ctx, fake, "/work"); err == nil || !strings.Contains(err.Error(), "could not be looked up") {
		t.Errorf("a name that does not resolve: %v", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("rclone ran for a refused endpoint: %v", calls)
	}
	// Amazon's own endpoint needs no pinning, and an address needs no name.
	s = base
	if _, err := s.List(ctx, fake, "/work"); err != nil && strings.Contains(err.Error(), "cannot be used") {
		t.Errorf("no endpoint: %v", err)
	}
	s.Endpoint = "http://198.51.100.9:9000"
	s.List(ctx, fake, "/work")
	for _, c := range fake.Calls() {
		if strings.Contains(c, "--add-host") {
			t.Errorf("an address was pinned although there is no name to pin: %s", c)
		}
	}
}
