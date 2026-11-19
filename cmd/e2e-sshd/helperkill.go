package main

// The far host's kill switch for the helper daemon it runs.
//
// # The scenario this exists for
//
// The far helper is a daemon installed under this account's home and started by
// the bridge a coordinator's exec lane runs; its SSH transport is the
// connection that lane rides. Lifecycle scenario 4 (design §5.10) is that
// daemon dying while the transport lives: the panels' helper axis turns
// `helper-lost` with a Refresh that redials, and the open shell is untouched.
// The e2e stand has to be able to produce that death ON CUE, at a moment a
// spec chooses, and nothing else may be touched — in particular not a bridge,
// which is also a process named after the same installed binary, and not
// another run's daemon, which may live under a different home on the same
// machine.
//
// # Why the far host kills it
//
// The fault belongs to the far side, exactly as the wire faults belong to the
// proxy in front of it (design §10). A spec reaching into this machine's
// process table would be staging a fault in the wrong place and would have to
// know which processes are whose; the fixture already IS the account whose home
// the daemon was installed into, so it can answer "whose daemon is this" from
// the command line itself.
//
// # The cue is the gate file's CONTENT, and it is published atomically
//
// A gate file that must appear (like -sftp-slow-until) cannot be re-armed
// without a race: remove-then-write is one state change the fixture's poll may
// never observe, and the second kill would silently not happen. So every
// distinct content is one cue, the same content is nothing, and a removed file
// forgets the last one. The spec writes a new nonce per kill.
//
// The writer publishes that content by renaming a temporary file onto the gate,
// never by truncating and rewriting it in place: a truncate-then-write leaves an
// EMPTY file for as long as the write takes, and a poll landing in that window
// would read blank content. The gate therefore refuses a blank file outright —
// a blank file is not a request — so even a writer that does not rename cannot
// turn a mid-update read into a kill.
//
// # What it announces, and why the lines exist
//
// `HELPERKILLED=<pid> cue=<cue>` per process signalled, then one
// `HELPERKILLDONE=<n> cue=<cue>`, printed in that order. A process table that
// cannot be read is announced as `HELPERKILLFAILED=<msg> cue=<cue>` and counted
// as zero; a kill that fails for one pid is not counted, because the far side
// only claims a death it caused.
//
// EVERY answer names the cue it answers, because the reader must wait for the
// answer to ITS OWN request. A reader that waits for the count of done lines to
// move can be satisfied by a late answer to the previous cue — the previous
// request's pid list, or its DONE=0 — and then act before this request has been
// looked at. The done line is the observable a spec waits on, so a wrong match
// fails at once instead of waiting out a timeout for a state the far side never
// reached.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// helperKillPollInterval is how often the gate looks at its file. It is the
// fixture's own clock, never a spec's: a spec waits for HELPERKILLDONE, not for
// this to tick.
const helperKillPollInterval = 50 * time.Millisecond

// helperKillGate watches the cue file and kills the far helper daemon of one
// account when it changes. list, kill and print are the outside world, injected
// so the gate's rules are testable without a process table.
type helperKillGate struct {
	path  string
	home  string
	print func(string)
	// list answers `ps` output for every process: pid and full command line.
	list func() ([]byte, error)
	kill func(pid int) error
	// seen is the gate content last acted on, and armed says whether there is
	// one. A file that was not there at the last look is not armed, so
	// re-creating it with the same content is a new cue.
	seen  string
	armed bool
}

// newHelperKillGate is the gate as run() uses it: the real process table, the
// real signal, and the fixture's own stdout.
func newHelperKillGate(path, home string) *helperKillGate {
	return &helperKillGate{
		path:  path,
		home:  home,
		print: printLine,
		list:  processTable,
		kill:  killProcess,
	}
}

// readCue answers the gate's current cue. Absent is no cue, and so is blank
// content: the writer renames a complete file onto the gate, but a writer that
// truncates in place leaves it empty while it writes, and a poll in that window
// must not read the gap as a request. The cue is the trimmed content, so the
// ordinary `echo nonce > cue` is a cue too.
func (g *helperKillGate) readCue() (string, bool) {
	data, err := os.ReadFile(g.path)
	if err != nil {
		return "", false
	}
	cue := strings.TrimSpace(string(data))
	return cue, cue != ""
}

// seed records a gate that was already there when the fixture started, without
// killing anything: a file that predates this run is not a cue this run
// received.
func (g *helperKillGate) seed() {
	cue, ok := g.readCue()
	if !ok {
		return
	}
	g.seen, g.armed = cue, true
}

// watch polls until the process ends. It does NOT seed: run() seeds before it
// says READY, so a cue written the moment the fixture is up cannot be mistaken
// for a file that was there before this run.
func (g *helperKillGate) watch() {
	ticker := time.NewTicker(helperKillPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		g.observe()
	}
}

// observe acts on one look at the gate: nothing for a file that is absent,
// blank or unchanged, one kill for each new cue.
func (g *helperKillGate) observe() {
	cue, ok := g.readCue()
	if !ok {
		g.seen, g.armed = "", false
		return
	}
	if g.armed && cue == g.seen {
		return
	}
	g.seen, g.armed = cue, true

	table, err := g.list()
	if err != nil {
		g.print(helperKillAnswer("HELPERKILLFAILED", fmt.Sprint(err), cue))
		g.print(helperKillAnswer("HELPERKILLDONE", "0", cue))
		return
	}
	killed := 0
	for _, pid := range g.daemons(string(table)) {
		if err := g.kill(pid); err != nil {
			continue
		}
		killed++
		g.print(helperKillAnswer("HELPERKILLED", strconv.Itoa(pid), cue))
	}
	g.print(helperKillAnswer("HELPERKILLDONE", strconv.Itoa(killed), cue))
}

// helperKillAnswer frames one answer as `<name>=<value> cue=<cue>`, so every
// line names the request it answers. The cue itself is flattened to one line:
// the reader compares the exact text it wrote, and a cue the status line cannot
// carry is not one the gate should have accepted.
func helperKillAnswer(name, value, cue string) string {
	return name + "=" + oneLine(value) + " cue=" + oneLine(cue)
}

// daemons answers this account's helper daemons in one process table, under
// every spelling of the home the table may carry them.
func (g *helperKillGate) daemons(table string) []int {
	seen := map[int]bool{}
	var pids []int
	for _, home := range accountHomes(g.home) {
		for _, pid := range helperDaemonPIDs(home, table) {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	return pids
}

// accountHomes answers the spellings of one home a process table may hold.
//
// On macOS a temporary directory is reached through a symlink and git — and
// SFTP's realpath, which is where the install path came from — answers the
// RESOLVED spelling. A match written against only the spelling this process was
// given finds nothing on the platform we ship, and a fault that finds nothing
// is a spec that fails somewhere else entirely (AGENTS.md, "On macOS a
// temporary directory is reached through a symlink").
func accountHomes(home string) []string {
	homes := []string{home}
	if resolved, err := filepath.EvalSymlinks(home); err == nil && resolved != home {
		homes = append(homes, resolved)
	}
	return homes
}

// helperDaemonPIDs answers the pids in a `ps` table whose command line is the
// helper daemon this account installed:
//
//	<home>/.nocx/helper/<generation>/nocx-helper serve
//
// The home is compared as text, so the caller may offer every spelling of it —
// macOS resolves a temporary directory through /private, and an install made
// through the resolved spelling is the same account's install.
//
// A bridge (the same binary, `bridge <generation>`) and an invocation with any
// further argument are deliberately not daemons: they are different processes
// with different lifetimes, and killing one is a different fault.
func helperDaemonPIDs(home, table string) []int {
	var pids []int
	for _, line := range strings.Split(table, "\n") {
		pidField, command, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidField)
		if err != nil {
			continue
		}
		command = strings.TrimLeft(command, " ")
		if !strings.HasPrefix(command, helperInstallPrefix(home)) {
			continue
		}
		if !strings.HasSuffix(command, string(os.PathSeparator)+"nocx-helper serve") {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// helperInstallPrefix is where an install of this account lives, with the
// separator that keeps one home from matching another that merely starts with
// its name.
func helperInstallPrefix(home string) string {
	return filepath.Join(home, ".nocx", "helper") + string(os.PathSeparator)
}

// processTable asks the operating system for every process's pid and command
// line. -ww is unlimited width: a truncated command would make the daemon's own
// path unreadable and the match silently empty.
func processTable() ([]byte, error) {
	out, err := exec.Command("ps", "-A", "-ww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return out, nil
}

// killProcess is the death the scenario is about: SIGKILL, so no drain, no
// shutdown path and no chance to hand a session over — the daemon is gone the
// way a crashed one is.
func killProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGKILL)
}

// oneLine keeps an error message inside one machine-readable line.
func oneLine(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}
