package profiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tailscale/hujson"
)

type change struct {
	path      string
	old, data []byte
	exists    bool
	mode      fs.FileMode
	command   *Operation
	replace   bool
}

// Plan contains checked file changes. Preparing a plan never writes files or
// runs client commands. Apply checks the files again before making changes.
type Plan struct {
	changes []change
	notes   []string
}

// SymlinkError identifies a target file that requires manual installation.
type SymlinkError struct{ Path, Target string }

func (e *SymlinkError) Error() string {
	return fmt.Sprintf("refusing symlink %s -> %s", e.Path, e.Target)
}

// Prepare checks all config and skill targets before any writes. Operations
// must already contain absolute file paths.
func Prepare(ops []Operation, skills fs.FS, skillDir string, force bool) (*Plan, error) {
	if !filepath.IsAbs(skillDir) {
		return nil, errors.New("skills directory must be absolute")
	}
	plan := &Plan{}
	var problems []error
	byPath := map[string]int{}
	for _, op := range ops {
		if !filepath.IsAbs(op.File) {
			return nil, errors.New("install target must be absolute")
		}
		i, found := byPath[op.File]
		if !found {
			c, err := readChange(op.File)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			c.data = c.old
			i = len(plan.changes)
			byPath[op.File] = i
			plan.changes = append(plan.changes, c)
		}
		c := &plan.changes[i]
		if found && (c.command != nil || op.Op == "command") {
			problems = append(problems, fmt.Errorf("command cannot share target %s with another operation", op.File))
			continue
		}
		if op.Op == "command" {
			c.command = &op
		}
		data, err := op.Edit(c.data, force)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", op.File, err))
			continue
		}
		c.data = data
		if op.Op == "command" && !bytes.Equal(c.old, c.data) {
			if c.exists {
				value, err := hujson.Parse(c.old)
				if err != nil {
					return nil, err
				}
				c.replace = value.Find(pointer(op.Path)) != nil
			}
			for _, argv := range [][]string{op.Run, op.Replace} {
				if len(argv) == 0 {
					problems = append(problems, errors.New("command has no argv"))
					continue
				}
				if _, err := exec.LookPath(argv[0]); err != nil {
					problems = append(problems, err)
				}
			}
		}
		if op.Note != "" {
			plan.notes = append(plan.notes, op.Note)
		}
	}
	err := fs.WalkDir(skills, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		target := filepath.Join(skillDir, filepath.FromSlash(path))
		if _, found := byPath[target]; found {
			return fmt.Errorf("config operation overlaps skill file %s", target)
		}
		c, err := readChange(target)
		if err != nil {
			return err
		}
		c.mode = 0o644
		c.data, err = fs.ReadFile(skills, path)
		if err != nil {
			return err
		}
		if c.exists && !bytes.Equal(c.old, c.data) && !force {
			problems = append(problems, fmt.Errorf("%s differs; pass --force to replace", target))
		}
		plan.changes = append(plan.changes, c)
		return nil
	})
	if err != nil {
		problems = append(problems, err)
	}
	for _, c := range plan.changes {
		if c.exists && !bytes.Equal(c.old, c.data) {
			if _, err := readChange(c.path + ".agora-bak"); err != nil {
				problems = append(problems, err)
			}
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return plan, nil
}

func readChange(path string) (change, error) {
	c := change{path: path, mode: 0o600}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return c, err
		}
		return c, &SymlinkError{Path: path, Target: target}
	}
	if !info.Mode().IsRegular() {
		return c, fmt.Errorf("%s is not a regular file", path)
	}
	c.exists = true
	c.mode = info.Mode().Perm()
	c.old, err = os.ReadFile(path)
	return c, err
}

// Apply writes checked changes, or reports them without writing for dryRun.
// Existing config modes are preserved; new configs and backups use 0600.
// Skills use 0644. Each file is backed up and written once.
func (p *Plan) Apply(ctx context.Context, dryRun bool, out io.Writer) error {
	for _, c := range p.changes {
		now, err := readChange(c.path)
		if err != nil {
			return err
		}
		if now.exists != c.exists || !bytes.Equal(now.old, c.old) {
			return fmt.Errorf("%s changed after preflight; retry", c.path)
		}
	}
	for _, c := range p.changes {
		if bytes.Equal(c.old, c.data) {
			fmt.Fprintln(out, "unchanged", c.path)
			continue
		}
		if dryRun {
			fmt.Fprintln(out, "would write", c.path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
			return err
		}
		if c.exists {
			created, err := keepBackup(c.path+".agora-bak", c.old)
			if err != nil {
				return err
			}
			action := "kept backup"
			if created {
				action = "backup"
			}
			fmt.Fprintln(out, action, c.path+".agora-bak", "(may contain credentials)")
		}
		if c.command != nil {
			if c.replace {
				if err := runArgv(ctx, c.command.Replace, c.command.CommandEnv); err != nil {
					return err
				}
			}
			if err := runArgv(ctx, c.command.Run, c.command.CommandEnv); err != nil {
				return err
			}
			// Check the actual result; the client may use a different config location.
			actualFile, err := readChange(c.path)
			if err != nil {
				return err
			}
			actual := actualFile.old
			checked, err := c.command.Edit(actual, false)
			if err != nil {
				return fmt.Errorf("command did not install expected entry: %w", err)
			}
			if !bytes.Equal(checked, actual) {
				return errors.New("command did not install expected entry")
			}
			if err := os.Chmod(c.path, c.mode); err != nil {
				return err
			}
		} else if err := writeFile(c.path, c.data, c.mode); err != nil {
			return err
		}
		fmt.Fprintln(out, "wrote", c.path)
	}
	for _, note := range p.notes {
		fmt.Fprintln(out, note)
	}
	return nil
}

// keepBackup retains the first pre-install contents across later updates.
func keepBackup(path string, data []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}

func runArgv(ctx context.Context, argv []string, overrides map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Client output may echo existing credentials; do not forward it.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = commandEnv(overrides)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s command failed: %w", argv[0], err)
	}
	return nil
}

func writeFile(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agora-install-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// commandEnv binds a client command to the same home used by path resolution.
// Empty overrides remove inherited variables; clients may interpret KEY=
// differently from an unset variable.
func commandEnv(overrides map[string]string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := overrides[key]; !ok {
			env = append(env, entry)
		}
	}
	var keys []string
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if overrides[key] != "" {
			env = append(env, key+"="+overrides[key])
		}
	}
	return env
}
