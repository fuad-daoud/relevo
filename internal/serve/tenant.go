package serve

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// Layout modes for the root-owned split: root owns the bindings tree, and a
// tenant owns only out/, .worktrees/ and its repos/<hex> and tmp/<hex>.
const (
	// serveRootMode is the mode of <root> and its bindings/, repos/ and tmp/
	// children: traversable so a tenant can reach its own directories, not
	// listable.
	serveRootMode = 0o711
	// ownerRootMode is the mode of bindings/<hex>: root-owned, group-tenant, so
	// the tenant may traverse but not replace its entries.
	ownerRootMode = 0o710
	// tenantDirMode is the mode of every directory the tenant owns outright.
	tenantDirMode = 0o700
)

// tenantFor resolves and sets up the tenant a user-mode owner's builders run
// as. In none mode it returns (nil, nil). In user mode it reads the owner's
// declared unix_user, resolves it with cfg.LookupUser, and creates or verifies
// the owner's split layout. Every missing prerequisite is returned as an error,
// which runtimeAt turns into a boundary-setup refusal and a round halt that
// names the fix.
func (s *Server) tenantFor(owner remote.ClientID) (*isolate.Tenant, error) {
	if s.cfg.Isolation != isolate.ModeUser {
		return nil, nil
	}
	cl, ok := s.clientOf(owner)
	if !ok {
		return nil, fmt.Errorf("no enrolled client %s", owner)
	}
	t, err := CheckUnixUser(s.cfg.LookupUser, cl.UnixUser)
	if err != nil {
		return nil, err
	}
	if err := s.ensureTenantRoots(owner, t); err != nil {
		return nil, err
	}
	return &t, nil
}

// clientOf returns the enrolled client with id, re-reading the registry so a
// concurrent enroll is seen live.
func (s *Server) clientOf(id remote.ClientID) (Client, bool) {
	for _, cl := range s.clients.List() {
		if cl.ID == id {
			return cl, true
		}
	}
	return Client{}, false
}

// tenantEnv is the login environment a user-mode process runs with: the
// tenant's own HOME/USER/LOGNAME, appended after the inherited environment with
// the inherited copies denied.
func tenantEnv(t isolate.Tenant) []string {
	return []string{"HOME=" + t.Home, "USER=" + t.User, "LOGNAME=" + t.User}
}

// ownerTransport builds the bundle transport for one user-mode owner: its temp
// bundles live in <root>/tmp/<hex> and are Lchown'ed to the tenant before git,
// now running as the tenant, touches them.
func (s *Server) ownerTransport(root string, t isolate.Tenant, gc *git.Client) remote.TreeTransport {
	dir := filepath.Join(s.cfg.Root, "tmp", filepath.Base(root))
	return remote.NewBundleTransport(gc, dir, remote.WithOwnerTmp(dir, t.UID, t.GID))
}

// ensureTenantRoots creates or verifies the split layout for one owner:
//
//	<root>, bindings/, repos/, tmp/          root 0711
//	bindings/<hex>                           root:<gid> 0710
//	bindings/<hex>/.worktrees, .../.scratch  tenant 0700
//	repos/<hex>, tmp/<hex>                   tenant 0700
//
// Every component is Lstat'ed first, so a symlink or a regular file where a
// directory belongs is refused, never followed. It runs on every resolution
// because the daemon's prunes remove the empty tenant directories.
func (s *Server) ensureTenantRoots(owner remote.ClientID, t isolate.Tenant) error {
	hex, ok := owner.Dir()
	if !ok {
		return errors.New("malformed client id")
	}
	root := s.cfg.Root
	for _, d := range []string{
		root,
		filepath.Join(root, "bindings"),
		filepath.Join(root, "repos"),
		filepath.Join(root, "tmp"),
	} {
		if err := ensureRootDir(d, serveRootMode); err != nil {
			return err
		}
	}
	ownerRoot := filepath.Join(root, "bindings", hex)
	if err := s.ensureOwnerRoot(ownerRoot, t); err != nil {
		return err
	}
	for _, d := range []string{
		filepath.Join(ownerRoot, ".worktrees"),
		filepath.Join(ownerRoot, ".worktrees", ".scratch"),
		filepath.Join(root, "repos", hex),
		filepath.Join(root, "tmp", hex),
	} {
		if err := ensureTenantDir(d, t); err != nil {
			return err
		}
	}
	return nil
}

// ensureRootDir creates a root-owned directory with mode when absent, and
// requires an existing path to be a real directory with that mode. It chmods an
// existing directory to mode so an upgraded install reaches 0711, which the
// tenant needs to traverse the serve root.
func ensureRootDir(path string, mode os.FileMode) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
		return os.Chmod(path, mode)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if fi.Mode().Perm() != mode {
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
	}
	return nil
}

// ensureOwnerRoot creates or verifies bindings/<hex>: root-owned, group the
// tenant's primary gid, mode 0710. Handing the group to the tenant lets it
// traverse to its own directories while leaving the binding entries
// unswappable; a wrong owner, group or mode is refused with the exact fix.
func (s *Server) ensureOwnerRoot(path string, t isolate.Tenant) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, ownerRootMode); err != nil {
			return err
		}
		if err := os.Chmod(path, ownerRootMode); err != nil {
			return err
		}
		fi, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	// Hand root:<gid> ownership. Lchown needs root; a non-root daemon (only a
	// test) cannot chown, so its EPERM is tolerated and the checks below then
	// read whatever owner the directory already has.
	if err := os.Lchown(path, 0, int(t.GID)); err != nil && os.Geteuid() == 0 {
		return fmt.Errorf("%s: chown root:%d: %w", path, t.GID, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm() != ownerRootMode {
		return fmt.Errorf("%s has mode %04o, want %04o: run `chmod %04o %s`", path, st.Mode().Perm(), ownerRootMode, ownerRootMode, path)
	}
	uid, gid, ok := statOwner(st)
	if !ok {
		return nil
	}
	if int(gid) != int(t.GID) {
		return fmt.Errorf("%s is group %d, want the tenant group %d: run `chown root:%d %s`", path, gid, t.GID, t.GID, path)
	}
	if os.Geteuid() == 0 && uid != 0 {
		return fmt.Errorf("%s is owned by uid %d, want root: run `chown root:%d %s`", path, uid, t.GID, path)
	}
	return nil
}

// ensureTenantDir creates a tenant-owned directory with mode 0700 when absent
// and hands it to the tenant; an existing symlink or non-directory is refused.
func ensureTenantDir(path string, t isolate.Tenant) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, tenantDirMode); err != nil {
			return err
		}
		if err := os.Chmod(path, tenantDirMode); err != nil {
			return err
		}
		fi, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if err := os.Lchown(path, int(t.UID), int(t.GID)); err != nil && os.Geteuid() == 0 {
		return fmt.Errorf("%s: chown %d:%d: %w", path, t.UID, t.GID, err)
	}
	return nil
}
