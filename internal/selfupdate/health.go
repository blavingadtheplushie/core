package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// SourcePointer is the file that redirects SOURCE_DIR after a relocation
// (read by config.Load when SOURCE_DIR is not set explicitly).
func SourcePointer(dataDir string) string { return filepath.Join(dataDir, "source.path") }

type WriteCheck struct {
	Writable bool   `json:"writable"`
	Failures int    `json:"failures"`
	Example  string `json:"example,omitempty"`
	Cause    string `json:"cause,omitempty"`
}

func selinuxLabel(path string) string {
	buf := make([]byte, 256)
	n, err := syscall.Getxattr(path, "security.selinux", buf)
	if err != nil || n <= 0 {
		return ""
	}
	return strings.TrimRight(string(buf[:n]), "\x00")
}

func processLabel() string {
	b, err := os.ReadFile("/proc/self/attr/current")
	if err != nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(string(b)), "\x00")
}

// diagnose explains why path is not writable.
func diagnose(path string, werr error) string {
	var parts []string
	if werr != nil {
		parts = append(parts, werr.Error())
	}
	if fi, err := os.Lstat(path); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			parts = append(parts, fmt.Sprintf("owner %d:%d, mode %s, process runs as %d:%d", st.Uid, st.Gid, fi.Mode().Perm(), os.Geteuid(), os.Getegid()))
		}
	}
	var sfs syscall.Statfs_t
	if syscall.Statfs(path, &sfs) == nil && sfs.Flags&1 != 0 { // ST_RDONLY
		parts = append(parts, "the filesystem is mounted read-only")
	}
	if lbl := selinuxLabel(path); lbl != "" {
		parts = append(parts, fmt.Sprintf("SELinux label %s (process: %s)", lbl, processLabel()))
	}
	return strings.Join(parts, "; ")
}

// CheckWritable verifies that the agent can create files in dir and modify
// every existing file in it (the agent edits its own code in place).
func CheckWritable(dir string) WriteCheck {
	c := WriteCheck{Writable: true}
	probe := filepath.Join(dir, ".blv-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		c.Writable, c.Failures, c.Example, c.Cause = false, 1, dir, diagnose(dir, err)
	} else {
		os.Remove(probe)
	}
	seen := 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || seen > 50000 {
			return nil
		}
		seen++
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		// Git writes its objects read-only on purpose and never edits them.
		if d.IsDir() && d.Name() == "objects" && filepath.Base(filepath.Dir(p)) == ".git" {
			return filepath.SkipDir
		}
		if aerr := syscall.Access(p, 2 /* W_OK */); aerr != nil {
			if c.Writable {
				c.Example, c.Cause = p, diagnose(p, aerr)
			}
			c.Writable = false
			c.Failures++
		}
		return nil
	})
	return c
}

// EnsureWritable makes sure the agent can edit its own source. It repairs
// ownership/permissions when running as root, and as a last resort copies
// the tree to a fresh directory (new files get the container's ownership and
// SELinux label) and switches SOURCE_DIR to it. The returned notice, if any,
// should be shown to the admins.
func (m *Manager) EnsureWritable(ctx context.Context) string {
	dir := m.cfg.SourceDir
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	check := CheckWritable(dir)
	if check.Writable {
		return ""
	}
	slog.Warn("source dir not writable", "dir", dir, "failures", check.Failures, "example", check.Example, "cause", check.Cause)

	if os.Geteuid() == 0 {
		fixed := 0
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if syscall.Access(p, 2) != nil {
				os.Lchown(p, 0, 0)
				if fi, err := os.Lstat(p); err == nil {
					os.Chmod(p, fi.Mode().Perm()|0o200|map[bool]fs.FileMode{true: 0o100, false: 0}[d.IsDir()])
				}
				fixed++
			}
			return nil
		})
		if CheckWritable(dir).Writable {
			msg := fmt.Sprintf("🔧 My source directory %s had %d file(s) I couldn't write (%s). I fixed their ownership/permissions.", dir, check.Failures, check.Cause)
			slog.Info("source dir repaired", "dir", dir, "fixed", fixed)
			return msg
		}
	}

	// Ownership is not the problem (SELinux label, odd mount…): rebuild the
	// tree from scratch next to it, then point SOURCE_DIR there.
	fresh := filepath.Join(m.cfg.DataDir, "src-"+time.Now().UTC().Format("20060102-150405"))
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if os.Getenv("SOURCE_DIR") != "" {
		return m.writeProblem(dir, check, "SOURCE_DIR is set explicitly, so I won't switch directories on my own")
	}
	how := "copied it to"
	if out, err := run(ctx, "", nil, "cp", "-r", "--no-preserve=all", dir+"/.", fresh); err != nil {
		// The old tree can't even be read (typically the SELinux label of
		// another container): start over from SELF_REPO or the image.
		os.RemoveAll(fresh)
		old := m.cfg.SourceDir
		m.cfg.SourceDir = fresh
		if serr := m.EnsureSource(m.seedDir); serr != nil {
			m.cfg.SourceDir = old
			os.RemoveAll(fresh)
			return m.writeProblem(dir, check, "copying it failed ("+truncate(out, 200)+") and so did a fresh checkout: "+serr.Error())
		}
		m.cfg.SourceDir = old
		how = "can't even read it, so I rebuilt a fresh source tree (from " + map[bool]string{true: "SELF_REPO", false: "the image"}[m.cfg.SelfRepo != "" && m.cfg.GitHubToken != ""] + ") in"
	}
	if !CheckWritable(fresh).Writable {
		os.RemoveAll(fresh)
		return m.writeProblem(dir, check, "even a fresh copy inside the data volume is not writable — the volume itself is the problem")
	}
	if err := os.WriteFile(SourcePointer(m.cfg.DataDir), []byte(fresh), 0o644); err != nil {
		os.RemoveAll(fresh)
		return m.writeProblem(dir, check, "could not record the new location: "+err.Error())
	}
	m.cfg.SourceDir = fresh
	run(ctx, fresh, nil, "git", "config", "--global", "--add", "safe.directory", fresh)
	slog.Info("source dir relocated", "from", dir, "to", fresh)
	return fmt.Sprintf("🔧 I couldn't write to my source directory %s (%s). I %s %s and now work from there — nothing to do on your side. The old copy can be deleted.", dir, check.Cause, how, fresh)
}

func (m *Manager) writeProblem(dir string, c WriteCheck, why string) string {
	msg := fmt.Sprintf("⚠️ I can't write to my source directory %s, so I can't modify my own code. %d file(s) affected, e.g. %s: %s. I tried to fix it but %s. Fix it as root, e.g. `chown -R 0:0 %s` (Docker/Podman: through `exec -u 0`; Proxmox LXC: `pct exec <ctid> --`); with SELinux, `chcon -R --reference=<volume mountpoint> <volume mountpoint>/src`.",
		dir, c.Failures, c.Example, c.Cause, why, dir)
	slog.Error("source dir not writable", "detail", msg)
	return msg
}

// WriteStatus is a cheap check for self_info.
func (m *Manager) WriteStatus() WriteCheck {
	if _, err := os.Stat(m.cfg.SourceDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return WriteCheck{Writable: false, Cause: "the source directory does not exist"}
		}
		return WriteCheck{Writable: false, Cause: err.Error()}
	}
	return CheckWritable(m.cfg.SourceDir)
}
