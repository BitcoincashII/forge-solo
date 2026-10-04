package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// The bundled PostgreSQL programs read their arguments, and find their own folder, in the system's
// ANSI code page. A character outside it (a user name in another script, which C:\Users\<name>
// carries into every path here) reached them as "?", and the database never started ("program
// postgres is needed by initdb but was not found"). A move gives them every path in a form they can
// take (pgReachable).

// pgForm is path in a form the bundled PostgreSQL programs can take, part by part: each part with a
// character beyond ASCII is given by the short (8.3) name Windows keeps for it, which is ASCII, or
// else as it is when the code page holds it. A part that is neither is named in bad, with path
// itself as form. Short names are read part by part, as the drive stores them: GetShortPathName
// leaves a part long when the code page's look-alike of it would be a valid short name ("Pawel"
// for a name with a Polish l), though the drive keeps one.
func pgForm(path string) (form, bad string) {
	if isASCII(path) {
		return path, ""
	}
	vol := filepath.VolumeName(path)
	if !isASCII(vol) && !codePageHolds(vol) {
		return path, vol
	}
	out := []byte(vol)
	start := len(vol)
	for i := start; i <= len(path); i++ {
		if i < len(path) && !os.IsPathSeparator(path[i]) {
			continue
		}
		name := path[start:i]
		if !isASCII(name) {
			if s, err := shortName(path[:i]); err == nil && s != "" && isASCII(s) {
				name = s
			} else if !codePageHolds(name) {
				return path, name
			}
		}
		out = append(out, name...)
		if i < len(path) {
			out = append(out, path[i])
		}
		start = i + 1
	}
	return string(out), ""
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// pgReachable is the folder at path in a form the bundled PostgreSQL programs can take:
//   - the path itself, when the code page holds all of it, as 1.0.12 gave it;
//   - else the short (8.3) names the drive keeps, or the parts the code page holds (pgForm);
//   - else a junction named link in this account's folder under %ProgramData%\ForgeSolo\links,
//     which leads to the folder (linksDir). link is then that junction, which the caller removes
//     once PostgreSQL has stopped.
//
// It fails when none of these can be had: %ProgramData% outside the code page, or a junction that
// cannot be made.
func pgReachable(path, name string) (form, link string, err error) {
	if isASCII(path) || codePageHolds(path) {
		return path, "", nil
	}
	if form, bad := pgForm(path); bad == "" {
		return form, "", nil
	}
	dir, err := linksDir(true)
	if err != nil {
		return "", "", err
	}
	link = filepath.Join(dir, name)
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", "", fmt.Errorf("%s is there from before and cannot be removed: %v", link, err)
	}
	if err := makeJunction(link, path); err != nil {
		return "", "", fmt.Errorf("the junction %s to %s could not be made: %v", link, path, err)
	}
	// What the link leads to must be the folder itself, and nothing put there meanwhile.
	a, errA := os.Stat(link)
	b, errB := os.Stat(path)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		_ = os.Remove(link)
		return "", "", fmt.Errorf("the junction %s does not lead to %s (%v, %v)", link, path, errA, errB)
	}
	logf("old data: PostgreSQL cannot take the name of %s, so it is given the junction %s", path, link)
	return link, link, nil
}

// linksDir is this Windows account's folder for the junctions of a move:
// %ProgramData%\ForgeSolo\links\<8 hex digits of the SHA-256 of the account's SID>. Each account has
// its own, so that two accounts' moves never share one. It is made when create is set.
func linksDir(create bool) (string, error) {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		return "", errors.New("%ProgramData% is not set")
	}
	if !isASCII(pd) && !codePageHolds(pd) {
		return "", fmt.Errorf("%%ProgramData%% (%s) has characters the bundled PostgreSQL cannot take", pd)
	}
	key, err := accountKey()
	if err != nil {
		return "", fmt.Errorf("this Windows account's SID cannot be read: %v", err)
	}
	dir := filepath.Join(pd, "ForgeSolo", "links", key)
	if create {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// linkNames are the junctions a move can make: to the data folder and to the install folder.
var linkNames = []string{"data", "app"}

// removeLeftLinks removes the junctions a move cut short left behind (Forge Solo ended from Task
// Manager, a power cut), and this account's folder for them. os.Remove removes a junction only,
// never what it leads to.
func removeLeftLinks() {
	dir, err := linksDir(false)
	if err != nil {
		return
	}
	for _, n := range linkNames {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logf("old data: the junction %s left from before could not be removed: %v", filepath.Join(dir, n), err)
		}
	}
	_ = os.Remove(dir)
}

// pgReach is how the bundled PostgreSQL reaches the two folders a move needs: data, the data folder
// (pgdata, pglog.txt), and app, the install folder (pgsql\bin), in forms it can take, and the
// junctions made for them.
type pgReach struct {
	data, app string
	links     []string
}

// reachPostgres finds the forms of the data and install folders the bundled PostgreSQL can take.
func reachPostgres() (*pgReach, error) {
	r := &pgReach{}
	for _, f := range []struct {
		path, name string
		form       *string
	}{{dataDir, "data", &r.data}, {installDir, "app", &r.app}} {
		form, link, err := pgReachable(f.path, f.name)
		if err != nil {
			r.remove()
			return nil, err
		}
		*f.form = form
		if link != "" {
			r.links = append(r.links, link)
		}
	}
	return r, nil
}

// remove removes the junctions made for the move, and the folder that held them. os.Remove removes
// a junction only, never what it leads to.
func (r *pgReach) remove() {
	for _, l := range r.links {
		if err := os.Remove(l); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logf("old data: the junction %s could not be removed: %v", l, err)
		}
	}
	if len(r.links) > 0 {
		_ = os.Remove(filepath.Dir(r.links[0]))
	}
	r.links = nil
}

// pgCmd runs name, one of the bundled PostgreSQL programs, from pgsql\bin in the install folder's
// form r.app, hidden and below normal priority.
func pgCmd(r *pgReach, name string, args ...string) *exec.Cmd {
	c := hiddenPrio(belowNormal, name, args...)
	c.Path = filepath.Join(r.app, "pgsql", "bin", name)
	c.Args[0] = c.Path
	c.Dir = r.app
	return c
}
