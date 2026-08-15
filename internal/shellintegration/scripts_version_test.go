package shellintegration

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The scripts are installed into ~/.nocx once and then never rewritten while
// the installed VERSION file still matches the `version` constant — that
// short-circuit is the whole point of the marker, and it is checked before
// anything reads the script's content. So a script edited WITHOUT bumping
// `version` reaches nobody: every existing shell keeps sourcing the copy
// installed the last time the number changed, and the change is invisible in
// the product while every test that reads the embedded string stays green.
//
// Measured on 2026-08-01: the OSC 636 command-existence hook shipped, the
// backend binary carried the new script, `go test ./internal/shellintegration`
// passed, and the feature did nothing in the app — ~/.nocx/shell-integration.bash
// was still the 6504-byte copy from 2026-07-26 because `version` was left at
// "7". The defect is structural: nothing connected the script bytes to the
// number that governs whether they are delivered.
//
// This test connects them. Change a script and the digest below stops matching,
// which is a failure that can only be resolved by deciding what the version
// should be — which is exactly the decision that was skipped.
func TestScriptVersionTracksScriptContent(t *testing.T) {
	// sha256 over each script's name and bytes, in a fixed order.
	//
	// WHEN THIS FAILS: you changed a shell integration script. Bump `version`
	// in scripts.go, then add an entry here for the new version with the digest
	// the failure prints. Do not edit an existing entry — a released version
	// number describes one exact set of scripts, and rewriting it in place is
	// the same as not having the check.
	// The digest covers every script in the `scripts` map: a change to
	// nocx.posix without a bump strands installs exactly like a bash change
	// would.
	digests := map[string]string{
		"8":  "ca89bf20e58c0a4669ecfb0754173ce721e436273b0b06549c7e0162e9b06dc8",
		"9":  "26ee0a75cf83df3a773c97ee39265c96912629c4bcdb629edea51ba5bcc5529d",
		"10": "17c0fdf278e54cd6fea16aed814b9c96b0daaff6e5d54c6ced03ebd93fc111aa",
		"11": "85b6105438f141628de8a87ecf013c7fad3df8053c81bd7b65975d26405c6a72",
		// v12: the first prompt's snapshot wait is bounded by elapsed time
		// rather than by a count of sleeps whose real cost it could not see
		// (nocx-0ije).
		"12": "7cc1e5d1f4af02ffa13b8654804e94efc19c51627471668ee204e72605a52655",
		// v13: the payload encoder gained a fast path for names needing no
		// escaping. It was ~85ms of the ~104ms snapshot pipeline, in front of a
		// 250ms grace that a fresh tab gets exactly one shot at — a shell idle
		// in readline runs no traps, so a job that misses it waits for a prompt
		// the user may never produce (nocx-z9s9.16).
		"13": "00383f333efb2633efb5b039302b36d834ffba9364dfb3b3406f4779d2cd3041",
		// v14: the authenticated lifecycle channel (ADR-0024) — the shell
		// speaks hello/accept/start/complete/prompt_ready over a transport
		// that is not the tty, authenticated by the per-epoch capability
		// (nocx-u7uh.3).
		"14": "1db018fdd91b47676ba3e71d75b9ac3f02346dcb57d6c314f0ad3ce8d5936490",
		// v15: the hooks answer a refresh_request with an authenticated
		// snapshot at the next prompt boundary and restore a visible prompt
		// while the domain is desynchronized (ADR-0024 decision 7/9,
		// nocx-u7uh.9). The snapshot names no attempt — the shell never
		// learns attempt ids — so open attempts reconcile as unknown.
		"15": "462c239042f18b149f94d8349bce08d5354595869eba785965dbb7037346ce7a",
		// v16: the snapshot names the shell's own attempts — the shell mints
		// an id per command at start, the kernel learns it at attach and
		// resolves it as a per-attempt alias, and a completion lost inside a
		// corrupted region reconciles to its real status instead of to
		// unknown; zsh answers refresh_request the way bash does; POSIX sh
		// documents the omission as decided (nocx-u7uh.19).
		"16": "d706a17d13634c274fcb0618dfd22c4eacd4427744d9848e23a5aa38a81a22a1",
		// v17: the shell-minted attempt id carries the domain (s-<dom>-<n>)
		// instead of the PID (s-$$-<n>): PID spaces are not shared across
		// domains, so a docker exec / ssh shell sharing a low PID with
		// another domain's shell minted a colliding id and the kernel
		// rejected the second domain's first command (nocx-u7uh.19).
		"17": "5edf9b249dd194fc3c43cd21cbb2a2608378afebd0f2f318928a7448f8671779",
		// v18: the shipped scripts are comment-stripped at embed time
		// (nocx-z9s9.17) — same code, no prose — so every install rewrites
		// to the smaller bytes and the version test hashes exactly what
		// ships.
		"18": "7e6cac4c22db022dd78434c5d6ac911dc12b8651b6db527de8df57ab82bfe06f",
		// v19: the recovery seam (ADR-0024 decision 8) — a failed lifecycle
		// send at a prompt boundary clears the active latch, restores a
		// visible native prompt, and emits the one-shot recovery fence
		// (nocx-u7uh.15).
		"19": "008bf5b8f7a80a8be10c30eedc1c1e3eb4e269f30427c0fd3651254d51dcd84c",
		// v20: __nocx_snapshot_wait_ms is declared once per shell rather than
		// once per source. The rcfile re-sources the script on purpose, and a
		// readonly can be neither unset nor re-declared, so every local
		// enhanced session printed "readonly variable" as its first line
		// (nocx-u7uh.22).
		"20": "4171ef459ec928439c0268ec98e6204d89ee0f05c53d4effc67048509faa7ba0",
		// v21: the handshake wait is a real poll and the connect no longer
		// hijacks stderr (nocx-u7uh.10). `read -N 0` with a nonzero -t
		// returns immediately on an open fd, so a kernel that accepted the
		// connection but never answered left the shell blocked in dd with no
		// prompt; the bounded wait now polls with `read -t 0 -N 0` around
		// each sleep, and the connect's 2>/dev/null is scoped to a group —
		// unscoped, `exec` made it permanent and every restored native
		// prompt (decisions 8/9) was invisible, because readline writes the
		// prompt to stderr.
		"21": "94b686e116c401b4d393319972333bf49e406b2c2021344e550f878b9ad256ca",
		"22": "21de58ea754f1d0099c63934b2916704aa92eab74da2d29975dd2df8e2ec2dd6",
		"23": "0ea2de4602addb7f5240a62b3490981ee34332e90a257b0bc7490d2f039d6d31",
		// v24: the zsh tier gets the nested-domain machinery (nocx-u7uh.28)
		// — accept-line-widget interception and zsh's own descriptor
		// staging, porting the bash tier's nocx-u7uh.11 flow.
		"24": "d31066a947f5a56af583bb12ed936f910bb2fc4987682e85ce742ec31cde24ce",
		// v25: the zsh nested launch binds the child's stdin to the tty
		// (zle gives widget commands /dev/null, which EOF'd the child) and
		// runs the precmd chain at the widget's end (zle does not fire it
		// for consumed lines) — nocx-u7uh.28.
		"25": "9d9703fb279d9732d6db22ff1d89a2a4372a5c75714b739c032b4ebd288dc005",
		// v26: the hello declares max_frame 262144. The kernel→shell
		// direction carries the ssh child's bootstrap — a full remote
		// launcher with the bundle embedded — and at 64 KiB that frame was
		// never written, so the parent sat out its grant timeout and ran the
		// user's ssh conventionally (nocx-beib).
		"26": "ddc204fcee1a2e640b9f58dcbdb75dcd8bd3cf8a56621fc9a6ce0de45e86bc37",
		// v27: the reader is bounded by the same declaration the hello
		// advertises. v26 raised only the advertised number and left the
		// length check at 65536, so the grant frame was rejected before it
		// was parsed (nocx-beib).
		"27": "4e88fad6351032bb90b94c3e2c72774cb0c35d5473ee5dd793ee1e1a649d7482",
		// v28: the grant frame is parsed with a shortest-match expansion —
		// the longest-match form is quadratic and cost ten seconds on a
		// grant-sized frame (nocx-beib).
		"28": "acdddb0681bfb3ea80974d9ae348c2f0d4150275ef426150e4ae7cf525fea559",
		// v29: the bash-4-only staging constructs are `eval`ed, so bash 3.2
		// — macOS's /bin/bash — can parse the file it is being asked to
		// source. Before this, every macOS shell died on the `coproc` token
		// and started with no integration at all (nocx-cn86).
		"29": "8d73196b2b55a9635abea8f19962d68a122fce22fa6547565a1a5d97cd7b4ba7",
		"30": "e66d393681bfe9edccd1db4ab75ac2b2c54514e749e68c781842c0ff0012f46c",
		"31": "b3038f8e3645002530c7a6f297b7c37823c7531903cf98e03d5215b2e204d831",
		"32": "588baf2b8d723f742be326c94a7ff1918cd77faea4e5c9473790f03462a5c9c2",
		"33": "0d14eb04fd4bdec163d2816cf003b216a703284bdee5845aa1559bbd32571f7e",
		"34": "7ce61eeb1dbcd505b92ed443654fa4fd83fc19bfd313120cbf239d78d6b476fe",
		"35": "37b4b1df12693cb33cd4cce844a8de1a5f9dfa488661292f8f861bad87c2b7cc",
		"36": "dd5d790c78599442f7e9e13da49bb82182532e329c55504055f29d3b67ab2c64",
		// v37: the zsh tier emits the command-existence snapshot (nocx-qduc).
		// Without it a zsh session's completion never learned a single command
		// name, on the platform whose default login shell is zsh.
		"37": "bf26d29e03b5bc82f94b8828ae6f6a892bcb5d142f70f30314ecd78bb9c110e9",
		// v38: the accept-line chain calls a widget rather than a function name
		// (nocx-wwz0). With any of the common highlighting/autosuggestion
		// plugins installed, pressing Enter printed "No such widget" and the
		// command did not run.
		"38": "f6f2b6cf7509ee97806376d141edaae0c94dd5df69ef2734586ae521ecb0c37d",
		// v39: unsupported sudo implementations fail open before the parent
		// lifecycle is suspended, and nested Bash cannot inherit nocx's
		// internal extdebug through an exported BASHOPTS (nocx-kf5w).
		"39": "a4d1de89f1a3851ddb8f8c0fd050a52235cda23f3b3346678f0300a1455278b7",
	}

	h := sha256.New()
	for _, name := range []string{"scripts/nocx.bash", "scripts/nocx.zsh", "scripts/nocx.posix"} {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(scriptFor(t, name)))
		h.Write([]byte{0})
	}
	got := hex.EncodeToString(h.Sum(nil))

	want, ok := digests[version]
	if !ok {
		t.Fatalf("version %q has no recorded script digest.\n"+
			"Add this entry to the digests map above:\n\n\t%q: %q,\n",
			version, version, got)
	}
	if got != want {
		t.Fatalf("the shell integration scripts changed but version is still %q.\n"+
			"An installed ~/.nocx carrying VERSION=%s will never be rewritten, so the\n"+
			"change reaches no shell. Bump `version` in scripts.go and add:\n\n\t%q: %q,\n",
			version, version, "<new version>", got)
	}
}

// scriptFor returns the embedded content for a script path, so the test hashes
// exactly the bytes that get installed rather than re-reading the file from
// disk (which would pass even if the go:embed directive pointed elsewhere).
func scriptFor(t *testing.T, path string) string {
	t.Helper()
	switch path {
	case "scripts/nocx.bash":
		return bashScript
	case "scripts/nocx.zsh":
		return zshScript
	case "scripts/nocx.posix":
		return posixScript
	default:
		t.Fatalf("no embedded script for %q", path)
		return ""
	}
}

// TestFrameParseAvoidsLongestMatch pins the one expansion that made the ssh
// child feel broken (nocx-beib).
//
// `${frame##*pat}` scans for the LAST occurrence, which bash does by walking
// every position: measured 1.65 s on a 78 KiB frame, against 1 ms for the
// shortest-match form. The grant carrying a remote launcher is exactly that
// size, so the shell burned ten seconds between reading the frame and using
// it — the user saw a tab that sat still after typing `ssh host`, with
// everything else already fixed and no hint that the delay was ours.
//
// The check is deliberately narrow: only the frame variable, only the
// longest-match form. Elsewhere ## is on short strings and is the right
// tool.
func TestFrameParseAvoidsLongestMatch(t *testing.T) {
	for name, body := range scripts {
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, "__nocx_lc_frame##") {
				continue
			}
			t.Errorf("%s parses the frame with a longest-match expansion, which is "+
				"quadratic on a grant-sized frame: %s", name, strings.TrimSpace(line))
		}
	}
}
