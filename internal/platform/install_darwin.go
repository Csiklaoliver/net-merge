package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const sudoersPath = "/etc/sudoers.d/net-merge"

var safeUser = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Install (run as `sudo net-merge install`) copies this binary to a root-owned path and lets the
// invoking user run exactly `net-merge tray` and `net-merge cleanup` there without a password.
// No wildcards: with `tray *` any process of the user could pass `-log /etc/...` and make root
// overwrite files. Uninstall removes both.
func Install(uninstall bool) error {
	if !IsAdmin() {
		return errors.New("run: sudo net-merge install")
	}
	if uninstall {
		for _, p := range []string{sudoersPath, InstalledExe} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		fmt.Println("removed " + sudoersPath + " and " + InstalledExe + ". Turn off start at sign-in with `net-merge autostart off`.")
		return nil
	}
	user := os.Getenv("SUDO_USER")
	if user == "" || user == "root" || !safeUser.MatchString(user) {
		return errors.New("run it through sudo from your own account: sudo net-merge install")
	}
	if err := copyRootOwned(InstalledExe); err != nil {
		return err
	}
	rule := fmt.Sprintf("# Written by `net-merge install`: %s may start the tray without a password.\n"+
		"%s ALL=(root) NOPASSWD: %s tray, %s cleanup\n", user, user, InstalledExe, InstalledExe)
	tmp := sudoersPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(rule), 0o440); err != nil {
		return err
	}
	defer os.Remove(tmp)
	// A broken sudoers file locks everyone out of sudo, so only move it into place once it parses.
	if out, err := exec.Command("/usr/sbin/visudo", "-cf", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("sudoers rule rejected, nothing installed: %s", strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, sudoersPath); err != nil {
		return err
	}
	fmt.Printf("installed %s and %s.\nStart the tray with `net-merge tray` (no password), and at sign-in with `net-merge autostart on`.\n", InstalledExe, sudoersPath)
	return nil
}

// copyRootOwned puts this binary at dst, root:wheel 0755, refusing if any directory above it
// could be changed by a non-root user (the copy is run as root, so that would be a root hole).
func copyRootOwned(dst string) error {
	for d := filepath.Dir(dst); d != "/"; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err != nil {
			return err
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s is writable by non-root users; refusing to install a root-run binary there", d)
		}
	}
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if src == dst {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chown(tmp, 0, 0); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
