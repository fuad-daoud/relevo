package syncworker

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// TestMigrateRefusals pins the hello-shape refusals: a normal pipe, no bucket
// with no pull, and an empty mirror list.
func TestMigrateRefusals(t *testing.T) {
	t.Run("a migrate verb on a normal pipe refuses", func(t *testing.T) {
		path := tempReplica(t)
		b := backendFor(t, &localDriver{path: path}, "origin-a")
		if _, err := b.Migrate(); !errors.Is(err, ErrProtocol) {
			t.Errorf("migrate without a migrate hello = %v, want ErrProtocol", err)
		}
	})

	t.Run("no bucket refuses before any pull", func(t *testing.T) {
		path := tempReplica(t)
		seedFormatFiveRemote(t, path)
		driver := &localDriver{path: path}
		b := NewTursoBackend(driver)
		b.openStore = func(R2Config) blobstore.BlobStore { return blobstore.NewMemBlobStore(nil) }
		if err := b.Open(Spec{
			Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://scratch.example",
			Token: "the-token", MirrorTables: migrateMirror, Migrate: true,
		}); err != nil {
			t.Fatalf("open without R2: %v", err)
		}
		t.Cleanup(func() { _ = b.Close() })
		if _, err := b.Migrate(); !errors.Is(err, ErrNoR2) {
			t.Errorf("migrate without R2 = %v, want ErrNoR2", err)
		}
		if driver.pulls != 0 {
			t.Errorf("pulls = %d, want none: the refusal runs before any transfer", driver.pulls)
		}
	})

	t.Run("an empty mirror list refuses", func(t *testing.T) {
		path := tempReplica(t)
		seedFormatFiveRemote(t, path)
		driver := &localDriver{path: path}
		b := NewTursoBackend(driver)
		store := blobstore.NewMemBlobStore(nil)
		b.openStore = func(R2Config) blobstore.BlobStore { return store }
		if err := b.Open(Spec{
			Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://scratch.example",
			Token: "the-token", R2: &R2Config{Bucket: "bucket"}, Migrate: true,
		}); err != nil {
			t.Fatalf("open with no mirror tables: %v", err)
		}
		t.Cleanup(func() { _ = b.Close() })
		if _, err := b.Migrate(); !errors.Is(err, ErrProtocol) {
			t.Errorf("migrate without mirror tables = %v, want ErrProtocol", err)
		}
		if !strings.Contains(errString(b), "mirror") {
			t.Errorf("refusal does not say the mirror list is missing")
		}
	})
}

// errString runs Migrate for its message only when the caller already asserted
// the error; it exists so the mirror test names the tables without repeating
// the call.
func errString(b *TursoBackend) string {
	_, err := b.Migrate()
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestMigrateSurfacesTheOldRefusals pins that a remote the migrate hello
// cannot finish is refused with the same class a migrate hello gives: stuck
// mid-migration, too old, and not a log.
func TestMigrateSurfacesTheOldRefusals(t *testing.T) {
	t.Run("a migration past the drop is stuck", func(t *testing.T) {
		path := tempReplica(t)
		seedFormatFiveRemote(t, path)
		store := blobstore.NewMemBlobStore(nil)
		b, _ := openMigrateBackend(t, path, store, "origin-a")
		execOn(t, path,
			"UPDATE meta SET value = '"+migratingFormat+"' WHERE key = '"+metaFormatKey+"'",
			createHeadsStatement,
			createSharedStatement,
			"DROP TABLE marks",
		)
		_, err := b.Migrate()
		if err == nil {
			t.Fatal("migrate past the drop reported no error")
		}
		if code := RefusalCodeOf(err); code != CodeRemote {
			t.Errorf("code = %q, want %q", code, CodeRemote)
		}
		if !strings.Contains(err.Error(), "mid-migration") {
			t.Errorf("refusal %q does not say the remote is stuck mid-migration", err)
		}
	})

	t.Run("a format the verb does not migrate is too old", func(t *testing.T) {
		path := tempReplica(t)
		seedFormatFourRemote(t, path)
		store := blobstore.NewMemBlobStore(nil)
		// A migrate hello refuses format 4 at open, so the surfacing is
		// pinned there: the verb never opens what it cannot finish.
		b := NewTursoBackend(&localDriver{path: path})
		b.openStore = func(R2Config) blobstore.BlobStore { return store }
		err := b.Open(Spec{
			Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://scratch.example",
			Token: "the-token", R2: &R2Config{Bucket: "bucket"},
			MirrorTables: migrateMirror, Migrate: true,
		})
		if err == nil {
			t.Fatal("a migrate hello opened format 4")
		}
		if code := RefusalCodeOf(err); code != CodeRemote {
			t.Errorf("code = %q, want %q", code, CodeRemote)
		}
		if !strings.Contains(err.Error(), "format 5 only") {
			t.Errorf("refusal %q does not say only format 5 migrates", err)
		}
	})

	t.Run("an unrelated remote is not a log", func(t *testing.T) {
		path := tempReplica(t)
		seedFormatFiveRemote(t, path)
		store := blobstore.NewMemBlobStore(nil)
		b, _ := openMigrateBackend(t, path, store, "origin-a")
		execOn(t, path, "CREATE TABLE extra (x TEXT)")
		_, err := b.Migrate()
		if err == nil {
			t.Fatal("migrate on an unrelated remote reported no error")
		}
		if code := RefusalCodeOf(err); code != CodeRemote {
			t.Errorf("code = %q, want %q", code, CodeRemote)
		}
		if !strings.Contains(err.Error(), "not a relevo sync log") {
			t.Errorf("refusal %q does not say the remote is not a log", err)
		}
	})
}
