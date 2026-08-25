package profile

// The role layer (bead nocx-e6kn2): the closed role enum, the assignment
// store, and THE ONE resolver. The tests pin the product rules the bead
// names — a role with no assignment is a refusal, never a fallback; a
// deleted endpoint or removed model leaves the role unresolvable and says
// so, never resolving to a neighbour.

import (
	"errors"
	"strings"
	"testing"
)

func TestAllRoles_IsTheClosedSetInOrder(t *testing.T) {
	got := AllRoles()
	want := []ModelRole{RoleAnswering, RoleClassifier}
	if len(got) != len(want) {
		t.Fatalf("AllRoles() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllRoles()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseModelRole(t *testing.T) {
	if r, err := ParseModelRole("answering"); err != nil || r != RoleAnswering {
		t.Fatalf("ParseModelRole(answering) = %q, %v", r, err)
	}
	if r, err := ParseModelRole("classifier"); err != nil || r != RoleClassifier {
		t.Fatalf("ParseModelRole(classifier) = %q, %v", r, err)
	}
	if _, err := ParseModelRole("gpt-4o"); !errors.Is(err, ErrRoleUnknown) {
		t.Fatalf("ParseModelRole(gpt-4o) = %v, want ErrRoleUnknown", err)
	}
}

func TestAssignRole_UpsertsOneAssignmentPerRole(t *testing.T) {
	s := newTestStore(t)
	first := RoleAssignment{Role: RoleAnswering, EndpointID: "endpoint:custom:a:1", Model: "gpt-4o"}
	if err := s.AssignRole(first); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	second := RoleAssignment{Role: RoleAnswering, EndpointID: "endpoint:custom:b:2", Model: "qwen3"}
	if err := s.AssignRole(second); err != nil {
		t.Fatalf("AssignRole (replace): %v", err)
	}
	got, err := s.LoadRoleAssignments()
	if err != nil {
		t.Fatalf("LoadRoleAssignments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("assignments = %+v, want exactly one row for the role", got)
	}
	if got[0].Model != "qwen3" || got[0].EndpointID != "endpoint:custom:b:2" {
		t.Errorf("assignment = %+v, want the SECOND assignment to have replaced the first", got[0])
	}
}

func TestAssignRole_RefusesUnknownRoleAndEmptyPair(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []RoleAssignment{
		{Role: "invented", EndpointID: "e", Model: "m"},
		{Role: RoleAnswering, EndpointID: "", Model: "m"},
		{Role: RoleAnswering, EndpointID: "e", Model: ""},
	} {
		if err := s.AssignRole(tc); err == nil {
			t.Errorf("AssignRole(%+v) succeeded, want a refusal", tc)
		}
	}
	if got, _ := s.LoadRoleAssignments(); len(got) != 0 {
		t.Fatalf("refused assignments must not be stored, got %+v", got)
	}
}

func validRoleEndpoints() []Endpoint {
	return []Endpoint{
		{
			ID:      "endpoint:custom:openai:111",
			Name:    "OpenAI",
			BaseURL: "https://api.openai.com/v1",
			Schema:  EndpointSchemaOpenAICompatible,
			Models:  []EndpointModel{{Name: "gpt-4o"}, {Name: "gpt-4o-mini"}},
		},
		{
			ID:      "endpoint:custom:local:222",
			Name:    "Local",
			BaseURL: "http://127.0.0.1:11434/v1",
			Schema:  EndpointSchemaOpenAICompatible,
			Models:  []EndpointModel{{Name: "qwen3"}},
		},
	}
}

func TestAssignRole_ClearRemovesTheAssignment(t *testing.T) {
	s := newTestStore(t)
	if err := s.AssignRole(RoleAssignment{Role: RoleAnswering, EndpointID: "endpoint:custom:a:1", Model: "gpt-4o"}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	// The empty pair is the CLEAR write: the role returns to the visible
	// "no model assigned" state and resolution refuses again.
	if err := s.AssignRole(RoleAssignment{Role: RoleAnswering}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ := s.LoadRoleAssignments(); len(got) != 0 {
		t.Fatalf("assignments after clear = %+v, want none", got)
	}
	// Clearing an already-clear role succeeds without storing anything.
	if err := s.AssignRole(RoleAssignment{Role: RoleAnswering}); err != nil {
		t.Fatalf("clear of a clear role: %v", err)
	}
	// A HALF-clear (one field set) is still refused: a role is assigned to
	// an (endpoint, model) pair or to nothing.
	if err := s.AssignRole(RoleAssignment{Role: RoleAnswering, Model: "gpt-4o"}); err == nil {
		t.Fatal("half-clear assignment succeeded, want a refusal")
	}
}

func TestResolveRole_ReturnsTheAssignedPair(t *testing.T) {
	eps := validRoleEndpoints()
	assignments := []RoleAssignment{
		{Role: RoleAnswering, EndpointID: "endpoint:custom:local:222", Model: "qwen3"},
	}
	ep, model, err := ResolveRole(RoleAnswering, assignments, DefaultModel{}, eps)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if model != "qwen3" {
		t.Errorf("model = %q, want qwen3", model)
	}
	if ep.ID != "endpoint:custom:local:222" {
		t.Errorf("endpoint = %s, want the assigned one", ep.ID)
	}
}

// The product rule (bead acceptance 2): a role with no model assigned is a
// visible refusal, never a silent fallback to another model — even when an
// unassigned endpoint with models exists right there.
func TestResolveRole_UnassignedIsARefusalNeverAFallback(t *testing.T) {
	_, _, err := ResolveRole(RoleAnswering, nil, DefaultModel{}, validRoleEndpoints())
	if !errors.Is(err, ErrRoleUnassigned) {
		t.Fatalf("ResolveRole without an assignment = %v, want ErrRoleUnassigned", err)
	}
	if !strings.Contains(err.Error(), string(RoleAnswering)) {
		t.Errorf("refusal %q must name the role", err)
	}
}

// Bead acceptance criterion 3: a deleted endpoint or removed model leaves
// the role unresolvable and SAYS so — never a hop to a neighbour.
func TestResolveRole_DeletedEndpointIsARefusalThatNamesIt(t *testing.T) {
	assignments := []RoleAssignment{
		{Role: RoleAnswering, EndpointID: "endpoint:custom:openai:111", Model: "gpt-4o"},
	}
	// The assigned endpoint is gone from the store; another endpoint remains.
	_, _, err := ResolveRole(RoleAnswering, assignments, DefaultModel{}, validRoleEndpoints()[1:])
	if !errors.Is(err, ErrRoleEndpointGone) {
		t.Fatalf("ResolveRole with a deleted endpoint = %v, want ErrRoleEndpointGone", err)
	}
	if !strings.Contains(err.Error(), "endpoint:custom:openai:111") {
		t.Errorf("error %q must name the deleted endpoint", err)
	}
}

func TestResolveRole_RemovedModelIsARefusalThatNamesIt(t *testing.T) {
	assignments := []RoleAssignment{
		{Role: RoleAnswering, EndpointID: "endpoint:custom:openai:111", Model: "gpt-4o"},
	}
	eps := []Endpoint{{
		ID:      "endpoint:custom:openai:111",
		Name:    "OpenAI",
		BaseURL: "https://api.openai.com/v1",
		Schema:  EndpointSchemaOpenAICompatible,
		// gpt-4o removed by an update; another model remains, which must NOT
		// be silently substituted.
		Models: []EndpointModel{{Name: "gpt-4o-mini"}},
	}}
	_, _, err := ResolveRole(RoleAnswering, assignments, DefaultModel{}, eps)
	if !errors.Is(err, ErrRoleModelGone) {
		t.Fatalf("ResolveRole with a removed model = %v, want ErrRoleModelGone", err)
	}
	if !strings.Contains(err.Error(), "gpt-4o") {
		t.Errorf("error %q must name the removed model", err)
	}
}

func TestResolveRole_UnknownRoleIsARefusal(t *testing.T) {
	_, _, err := ResolveRole("invented", nil, DefaultModel{}, nil)
	if !errors.Is(err, ErrRoleUnknown) {
		t.Fatalf("ResolveRole(invented) = %v, want ErrRoleUnknown", err)
	}
}

// The store keeps a dangling assignment (the delete clears SECRET
// references, never a role's endpoint name), so the row can show "no longer
// available" and the resolver's refusal can name what disappeared.
func TestAssignRole_DoesNotRequireTheEndpointToExist(t *testing.T) {
	s := newTestStore(t)
	if err := s.AssignRole(RoleAssignment{Role: RoleClassifier, EndpointID: "endpoint:custom:ghost:1", Model: "gpt-4o"}); err != nil {
		t.Fatalf("AssignRole to a not-yet-existing endpoint must succeed (shape is the write's check): %v", err)
	}
	got, err := s.LoadRoleAssignments()
	if err != nil || len(got) != 1 || got[0].EndpointID != "endpoint:custom:ghost:1" {
		t.Fatalf("assignments = %+v (err %v), want the dangling assignment stored", got, err)
	}
}

func TestRoleRepository_SurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/p.json"
	s := NewJSONStore(path)
	if err := s.AssignRole(RoleAssignment{Role: RoleClassifier, EndpointID: "e", Model: "m"}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	again := NewJSONStore(path)
	got, err := again.LoadRoleAssignments()
	if err != nil || len(got) != 1 || got[0].Role != RoleClassifier {
		t.Fatalf("reloaded assignments = %+v (err %v), want the stored row", got, err)
	}
}

// The default (bead nocx-rikz5): ONE (endpoint, model) pair a person names
// once, which every role WITHOUT its own assignment resolves through. It is
// an input to the one resolver, not a second resolution path, and it is
// legal only because the person authored it — "the first model of the first
// endpoint" is the fallback nocx-e6kn2 forbids.
func TestResolveRole_FallsBackToTheDefault(t *testing.T) {
	eps := []Endpoint{{ID: "e1", Name: "openrouter", Models: []EndpointModel{{Name: "m-a"}, {Name: "m-b"}}}}
	def := DefaultModel{EndpointID: "e1", Model: "m-a"}

	// No assignment at all: the default answers.
	ep, model, err := ResolveRole(RoleAnswering, nil, def, eps)
	if err != nil {
		t.Fatalf("resolve with only a default: %v", err)
	}
	if ep.ID != "e1" || model != "m-a" {
		t.Fatalf("resolved to %q/%q, want e1/m-a", ep.ID, model)
	}

	// An explicit assignment OUTRANKS the default — the override is the point.
	as := []RoleAssignment{{Role: RoleAnswering, EndpointID: "e1", Model: "m-b"}}
	_, model, err = ResolveRole(RoleAnswering, as, def, eps)
	if err != nil {
		t.Fatalf("resolve with an assignment: %v", err)
	}
	if model != "m-b" {
		t.Fatalf("resolved to %q, want the role's own m-b", model)
	}
}

func TestResolveRole_NoDefaultAndNoAssignmentStaysUnassigned(t *testing.T) {
	eps := []Endpoint{{ID: "e1", Name: "openrouter", Models: []EndpointModel{{Name: "m-a"}}}}
	_, _, err := ResolveRole(RoleAnswering, nil, DefaultModel{}, eps)
	if !errors.Is(err, ErrRoleUnassigned) {
		t.Fatalf("err = %v, want ErrRoleUnassigned", err)
	}
}

func TestResolveRole_ADefaultPointingAtNothingRefusesRatherThanRepairs(t *testing.T) {
	eps := []Endpoint{{ID: "e1", Name: "openrouter", Models: []EndpointModel{{Name: "m-a"}}}}

	// A default naming an endpoint that is not there refuses. This is the
	// RACE path, not the ordinary one: DeleteEndpoint clears the default in
	// the same write, so in the ordinary case there is no default left to
	// dangle. Kept because clearing is the tidy path, never the safety
	// net — another process may delete between load and resolve.
	gone := DefaultModel{EndpointID: "deleted", Model: "m-a"}
	if _, _, err := ResolveRole(RoleAnswering, nil, gone, eps); !errors.Is(err, ErrRoleEndpointGone) {
		t.Fatalf("deleted endpoint: err = %v, want ErrRoleEndpointGone", err)
	}

	stale := DefaultModel{EndpointID: "e1", Model: "m-removed"}
	if _, _, err := ResolveRole(RoleAnswering, nil, stale, eps); !errors.Is(err, ErrRoleModelGone) {
		t.Fatalf("removed model: err = %v, want ErrRoleModelGone", err)
	}
}

// A half-set default names nothing, so it is refused at the write; the
// EMPTY pair is the clear, returning every unassigned role to the visible
// failure state. "Unset" is a value, never an error, on the way back out.
func TestSetDefaultModel_TheEmptyPairClearsAndAHalfSetPairIsRefused(t *testing.T) {
	s := newTestStore(t)
	// The endpoint must EXIST for the pair to be storable at all: the store
	// refuses a default naming one it does not hold, so the fixture is a
	// real endpoint rather than two invented ids.
	ep := validTestEndpoint()
	if err := s.CreateEndpoint(ep); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	chosen := DefaultModel{EndpointID: ep.ID, Model: ep.Models[0].Name}

	if def, err := s.LoadDefaultModel(); err != nil || def.IsSet() {
		t.Fatalf("fresh store: default = %+v, err = %v, want the zero value and no error", def, err)
	}
	if err := s.SetDefaultModel(chosen); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	if def, _ := s.LoadDefaultModel(); def != chosen {
		t.Fatalf("default = %+v, want %+v", def, chosen)
	}
	for _, half := range []DefaultModel{{EndpointID: ep.ID}, {Model: ep.Models[0].Name}} {
		if err := s.SetDefaultModel(half); err == nil {
			t.Errorf("SetDefaultModel(%+v) succeeded, want a refusal", half)
		}
	}
	if def, _ := s.LoadDefaultModel(); def != chosen {
		t.Fatalf("a refused write changed the stored default to %+v", def)
	}
	if err := s.SetDefaultModel(DefaultModel{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if def, _ := s.LoadDefaultModel(); def.IsSet() {
		t.Fatalf("default after the clear = %+v, want unset", def)
	}
}

func TestDefaultModel_SurvivesReload(t *testing.T) {
	path := t.TempDir() + "/p.json"
	s := NewJSONStore(path)
	ep := validTestEndpoint()
	if err := s.CreateEndpoint(ep); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	chosen := DefaultModel{EndpointID: ep.ID, Model: ep.Models[0].Name}
	if err := s.SetDefaultModel(chosen); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	got, err := NewJSONStore(path).LoadDefaultModel()
	if err != nil || got != chosen {
		t.Fatalf("reloaded default = %+v (err %v), want the stored pair", got, err)
	}
}

func TestDeleteEndpoint_ClearsADefaultNamingItButLeavesAssignmentsDangling(t *testing.T) {
	s := newTestStore(t)
	ep := validTestEndpoint()
	if err := s.CreateEndpoint(ep); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	if err := s.SetDefaultModel(DefaultModel{EndpointID: ep.ID, Model: ep.Models[0].Name}); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	if err := s.AssignRole(RoleAssignment{Role: RoleClassifier, EndpointID: ep.ID, Model: ep.Models[0].Name}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if _, err := s.DeleteEndpoint(ep.ID); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}

	def, err := s.LoadDefaultModel()
	if err != nil {
		t.Fatalf("LoadDefaultModel: %v", err)
	}
	if def.IsSet() {
		t.Fatalf("the default survived its endpoint as %+v — it now points at nothing", def)
	}

	as, err := s.LoadRoleAssignments()
	if err != nil {
		t.Fatalf("LoadRoleAssignments: %v", err)
	}
	if len(as) != 1 || as[0].EndpointID != ep.ID {
		t.Fatalf("assignments = %+v, want the classifier's kept so the person is told it broke", as)
	}

	// And the product consequence, which is the criterion that matters: an
	// unassigned role is back at "choose a model", not "endpoint gone".
	if _, _, err := ResolveRole(RoleAnswering, as, def, nil); !errors.Is(err, ErrRoleUnassigned) {
		t.Fatalf("after the delete: err = %v, want ErrRoleUnassigned", err)
	}
	// ...while the role that DID name it is still told, by the resolver, that
	// what it named is gone.
	if _, _, err := ResolveRole(RoleClassifier, as, def, nil); !errors.Is(err, ErrRoleEndpointGone) {
		t.Fatalf("the dangling assignment: err = %v, want ErrRoleEndpointGone", err)
	}
}

// A default names an endpoint AND a model, and the store refuses a pair
// whose model that endpoint does not offer — the same refusal, at the same
// moment, as a pair whose endpoint does not exist (bead nocx-rikz5). The
// claim under test is "nothing was stored": a store that ALREADY HOLDS a
// valid default must still hold exactly that one afterwards, which an
// assertion on the error alone cannot show.
func TestSetDefaultModel_RefusesAModelTheEndpointDoesNotOfferAndKeepsTheStoredOne(t *testing.T) {
	s := newTestStore(t)
	ep := validTestEndpoint()
	if err := s.CreateEndpoint(ep); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	good := DefaultModel{EndpointID: ep.ID, Model: ep.Models[0].Name}
	if err := s.SetDefaultModel(good); err != nil {
		t.Fatalf("SetDefaultModel(valid): %v", err)
	}

	invented := DefaultModel{EndpointID: ep.ID, Model: "a-model-this-endpoint-never-offered"}
	err := s.SetDefaultModel(invented)
	if !errors.Is(err, ErrEndpointModelNotFound) {
		t.Fatalf("SetDefaultModel(%+v) = %v, want ErrEndpointModelNotFound", invented, err)
	}
	if def, _ := s.LoadDefaultModel(); def != good {
		t.Fatalf("the refused write left the default as %+v, want the original %+v — nothing may be stored on a refusal", def, good)
	}
}

// The endpoint's existence is checked against the SAME loaded document the
// write goes to, so validate-and-write is one operation under one lock: a
// concurrent DeleteEndpoint cannot land between a check and a write and
// leave a default naming an endpoint that is gone.
func TestSetDefaultModel_RefusesAnEndpointThatDoesNotExistAndKeepsTheStoredOne(t *testing.T) {
	s := newTestStore(t)
	ep := validTestEndpoint()
	if err := s.CreateEndpoint(ep); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	good := DefaultModel{EndpointID: ep.ID, Model: ep.Models[0].Name}
	if err := s.SetDefaultModel(good); err != nil {
		t.Fatalf("SetDefaultModel(valid): %v", err)
	}

	ghost := DefaultModel{EndpointID: "endpoint:custom:ghost:9", Model: ep.Models[0].Name}
	err := s.SetDefaultModel(ghost)
	if !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("SetDefaultModel(%+v) = %v, want ErrEndpointNotFound", ghost, err)
	}
	if def, _ := s.LoadDefaultModel(); def != good {
		t.Fatalf("the refused write left the default as %+v, want the original %+v", def, good)
	}
}
