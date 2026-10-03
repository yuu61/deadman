package configfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRewrite(t *testing.T) {
	const (
		original  = "h 192.0.2.1\n"
		formatted = "h  192.0.2.1\n"
	)

	path := writeRewriteConfig(t, original)

	chmodRewriteConfig(t, path, 0o640)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	err = Rewrite(path, []byte(original), formatted)
	if err != nil {
		t.Fatal(err)
	}

	assertRewriteConfig(t, path, formatted)

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if after.Mode() != before.Mode() {
		t.Errorf("mode = %v, want %v", after.Mode(), before.Mode())
	}

	if os.SameFile(before, after) {
		t.Error("file was overwritten rather than replaced")
	}
}

func TestRewriteUnchanged(t *testing.T) {
	const original = "h  192.0.2.1\n"

	for _, mode := range []fs.FileMode{0o600, 0o400} {
		t.Run(mode.String(), func(t *testing.T) { assertUnchangedRewrite(t, original, mode) })
	}
}

func assertUnchangedRewrite(t *testing.T, original string, mode fs.FileMode) {
	t.Helper()

	path := writeRewriteConfig(t, original)

	chmodRewriteConfig(t, path, mode)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	err = Rewrite(path, []byte(original), original)
	if err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Error("unchanged configuration was rewritten")
	}

	assertRewriteConfig(t, path, original)
}

func TestRewriteRejectsChangedInput(t *testing.T) {
	const original = "h 192.0.2.1\n"

	path := writeRewriteConfig(t, original)

	err := Rewrite(path, []byte("h 192.0.2.2\n"), "h  192.0.2.2\n")
	if err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("expected changed input to fail: %v", err)
	}

	assertRewriteConfig(t, path, original)
}

func TestRewriteReadOnly(t *testing.T) {
	const original = "h 192.0.2.1\n"

	path := writeRewriteConfig(t, original)

	err := os.Chmod(path, 0o400)
	if err != nil {
		t.Fatal(err)
	}

	err = Rewrite(path, []byte(original), "h  192.0.2.1\n")
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("expected read-only file to fail: %v", err)
	}

	assertRewriteConfig(t, path, original)
}

func TestRewriteSymlink(t *testing.T) {
	const original = "h 192.0.2.1\n"

	path := writeRewriteConfig(t, original)
	link := filepath.Join(t.TempDir(), "config.conf")

	err := os.Symlink(path, link)
	if err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err = Rewrite(link, []byte(original), "h  192.0.2.1\n")
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.Readlink(link)
	if err != nil || got != path {
		t.Errorf("symlink changed: %q, %v", got, err)
	}

	assertRewriteConfig(t, path, "h  192.0.2.1\n")
}

func TestCommitReplacementRejectsConcurrentChanges(t *testing.T) {
	const original = "h 192.0.2.1\n"

	for _, change := range []string{"content", "identity", "mode", "symlink"} {
		t.Run(change, func(t *testing.T) {
			path := writeRewriteConfig(t, original)
			if change == "symlink" {
				link := filepath.Join(t.TempDir(), "config.conf")

				err := os.Symlink(path, link)
				if err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}

				path = link
			}

			source, err := readRewriteSource(path)
			if err != nil {
				t.Fatal(err)
			}

			want := original

			switch change {
			case "content":
				want = "h 192.0.2.2\n"
				err = os.WriteFile(path, []byte(want), 0o600)
			case "identity":
				err = os.Rename(writeRewriteConfig(t, original), path)
			case "mode":
				if runtime.GOOS == "windows" {
					t.Skip("Windows only supports changing the read-only permission")
				}

				chmodRewriteConfig(t, path, 0o640)
			case "symlink":
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(writeRewriteConfig(t, original), path)
				}
			default:
				t.Fatalf("unknown change: %s", change)
			}

			if err != nil {
				t.Fatal(err)
			}

			temporary := writeRewriteConfig(t, "replacement\n")

			err = commitReplacement(path, temporary, source)
			if err == nil || !strings.Contains(err.Error(), "configuration changed") {
				t.Errorf("expected concurrent change to fail: %v", err)
			}

			assertRewriteConfig(t, path, want)
			assertRewriteConfig(t, temporary, "replacement\n")
		})
	}
}

func TestRewriteSourceErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.conf")

	err := Rewrite(missing, nil, "")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing source: %v", err)
	}

	err = Rewrite(t.TempDir(), nil, "")
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Errorf("expected directory to fail: %v", err)
	}
}

func TestRewriteUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions without root privileges")
	}

	const original = "h 192.0.2.1\n"

	path := writeRewriteConfig(t, original)
	dir := filepath.Dir(path)

	chmodRewriteConfig(t, dir, 0o500)
	t.Cleanup(func() { chmodRewriteConfig(t, dir, 0o700) })

	// Identical output must not need permission to create a temporary file.
	err := Rewrite(path, []byte(original), original)
	if err != nil {
		t.Errorf("unchanged file in unwritable directory: %v", err)
	}

	err = Rewrite(path, []byte(original), "h  192.0.2.1\n")
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("expected unwritable directory to fail: %v", err)
	}

	assertRewriteConfig(t, path, original)
}

func TestWriteReplacementFailure(t *testing.T) {
	const original = "h 192.0.2.1\n"

	path := writeRewriteConfig(t, original)

	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	file, err := root.Open(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}

	err = writeReplacement(file, "replacement\n", 0o600)
	if err == nil || !strings.Contains(err.Error(), "write formatted configuration") {
		t.Errorf("expected read-only handle to fail: %v", err)
	}

	assertRewriteConfig(t, path, original)
}

func TestCommitReplacementRenameFailure(t *testing.T) {
	const original = "h 192.0.2.1\n"

	path := writeRewriteConfig(t, original)

	source, err := readRewriteSource(path)
	if err != nil {
		t.Fatal(err)
	}

	err = commitReplacement(path, filepath.Join(t.TempDir(), "missing"), source)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected rename to fail: %v", err)
	}

	assertRewriteConfig(t, path, original)
}

func TestFinishRewriteIOFailures(t *testing.T) {
	const original = "h 192.0.2.1\n"

	for _, tc := range []struct {
		name     string
		writeErr error
		chmodErr error
		syncErr  error
		closeErr error
	}{
		{"write", io.ErrShortWrite, nil, nil, nil},
		{"chmod", nil, fs.ErrPermission, nil, nil},
		{"sync", nil, nil, io.ErrUnexpectedEOF, nil},
		{"close", nil, nil, nil, fs.ErrInvalid},
		{"write and close", io.ErrShortWrite, nil, nil, fs.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRewriteConfig(t, original)
			source, file := newFailingReplacement(t, path)

			file.writeErr, file.chmodErr = tc.writeErr, tc.chmodErr
			file.syncErr, file.closeErr = tc.syncErr, tc.closeErr

			err := finishRewrite(path, file, "h  192.0.2.1\n", source)
			for _, want := range []error{tc.writeErr, tc.chmodErr, tc.syncErr, tc.closeErr} {
				if want != nil && !errors.Is(err, want) {
					t.Errorf("missing failure %v: %v", want, err)
				}
			}

			assertRewriteConfig(t, path, original)
			assertReplacementClosed(t, file)
		})
	}
}

func TestFinishRewriteRejectsChangesAndCleansUp(t *testing.T) {
	const (
		original = "h 192.0.2.1\n"
		edited   = "h 192.0.2.2\n"
	)

	path := writeRewriteConfig(t, original)
	source, file := newFailingReplacement(t, path)
	file.afterClose = func() error { return os.WriteFile(path, []byte(edited), 0o600) }

	err := finishRewrite(path, file, "h  192.0.2.1\n", source)
	if err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Errorf("expected concurrent edit to fail: %v", err)
	}

	assertRewriteConfig(t, path, edited)
	assertReplacementClosed(t, file)
}

func TestFinishRewriteSourceRemoved(t *testing.T) {
	path := writeRewriteConfig(t, "h 192.0.2.1\n")
	source, file := newFailingReplacement(t, path)
	file.afterClose = func() error { return os.Remove(path) }

	err := finishRewrite(path, file, "h  192.0.2.1\n", source)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected removed source to fail: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 0 {
		t.Errorf("source was recreated or temporary file remains: %v, %v", entries, err)
	}

	assertReplacementClosed(t, file)
}

func TestFinishRewriteReportsCleanupFailure(t *testing.T) {
	const original = "h 192.0.2.1\n"

	path := writeRewriteConfig(t, original)
	source, file := newFailingReplacement(t, path)
	file.writeErr = io.ErrShortWrite
	file.afterClose = func() error {
		// Make removal fail deterministically, without OS permission assumptions.
		err := os.Remove(file.Name())
		if err != nil {
			return err
		}

		err = os.Mkdir(file.Name(), 0o700)
		if err != nil {
			return err
		}

		return os.WriteFile(filepath.Join(file.Name(), "blocker"), nil, 0o600)
	}

	err := finishRewrite(path, file, "h  192.0.2.1\n", source)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("lost original write failure: %v", err)
	}

	var cleanupError *os.PathError
	if !errors.As(err, &cleanupError) || cleanupError.Op != "remove" ||
		cleanupError.Path != file.Name() {
		t.Errorf("cleanup failure not reported: %v", err)
	}

	err = os.RemoveAll(file.Name())
	if err != nil {
		t.Fatal(err)
	}

	assertRewriteConfig(t, path, original)
	assertReplacementClosed(t, file)
}

type failingReplacement struct {
	*os.File

	writeErr   error
	chmodErr   error
	syncErr    error
	closeErr   error
	afterClose func() error
	closeCalls int
}

func (f *failingReplacement) WriteString(text string) (int, error) {
	if f.writeErr != nil {
		// Leave a real partial temporary file for the cleanup assertion.
		n, err := f.File.WriteString(text[:len(text)/2])

		return n, errors.Join(err, f.writeErr)
	}

	return f.File.WriteString(text)
}

func (f *failingReplacement) Chmod(mode fs.FileMode) error {
	if f.chmodErr != nil {
		return f.chmodErr
	}

	return f.File.Chmod(mode)
}

func (f *failingReplacement) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}

	return f.File.Sync()
}

func (f *failingReplacement) Close() error {
	f.closeCalls++
	err := f.File.Close()

	if f.afterClose != nil {
		err = errors.Join(err, f.afterClose())
	}

	return errors.Join(err, f.closeErr)
}

func newFailingReplacement(t *testing.T, path string) (rewriteSource, *failingReplacement) {
	t.Helper()

	source, err := readRewriteSource(path)
	if err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	file, err := root.OpenFile(".deadman-format-test", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		closeErr := file.Close()
		if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close temporary file: %v", closeErr)
		}
	})

	return source, &failingReplacement{File: file}
}

func assertReplacementClosed(t *testing.T, file *failingReplacement) {
	t.Helper()

	if file.closeCalls != 1 {
		t.Errorf("Close called %d times, want once", file.closeCalls)
	}

	_, err := file.Stat()
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("temporary file was not closed: %v", err)
	}
}

func writeRewriteConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "deadman.conf")

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func assertRewriteConfig(t *testing.T, path, want string) {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	data, err := root.ReadFile(filepath.Base(resolved))
	if err != nil || string(data) != want {
		t.Errorf("configuration = %q, %v; want %q", data, err, want)
	}

	entries, err := os.ReadDir(filepath.Dir(resolved))
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".deadman-format-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

func chmodRewriteConfig(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()

	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	err = root.Chmod(filepath.Base(path), mode)
	if err != nil {
		t.Fatal(err)
	}
}
