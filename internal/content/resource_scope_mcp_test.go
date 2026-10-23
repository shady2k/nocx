package content

import "testing"

func TestMCPStdioDestinationsAreExactOpaqueIdentities(t *testing.T) {
	exact := GrantScope{Kind: ResourceDestination, ID: "mcp+stdio:server-a"}
	if err := ValidateGrantScope(exact); err != nil {
		t.Fatal(err)
	}
	if !exact.Contains(exact) {
		t.Fatal("exact MCP identity does not contain itself")
	}
	for _, parent := range []GrantScope{
		{Kind: ResourceDestination, ID: "*"},
		{Kind: ResourceDestination, ID: "https://server-a", IncludeSubdomains: true},
		{Kind: ResourceDestination, ID: "mcp+stdio:server"},
		{Kind: ResourceDestination, ID: "mcp+stdio:server-a", IncludeSubdomains: true},
	} {
		if parent.Contains(exact) {
			t.Errorf("%+v authorized opaque MCP identity", parent)
		}
	}
	for _, id := range []string{"mcp+stdio:", "mcp+stdio:*", "mcp+stdio:server-a/tool", "mcp+stdio:server-a?x", "mcp+stdio:server-a#x", "mcp+stdio:server-a%2fchild"} {
		child := GrantScope{Kind: ResourceDestination, ID: id}
		if ValidateGrantScope(child) == nil {
			t.Errorf("accepted malformed identity %q", id)
		}
		if exact.Contains(child) {
			t.Errorf("exact identity contains %q", id)
		}
	}
	if exact.Contains(GrantScope{Kind: ResourceDestination, ID: "https://server-a"}) {
		t.Fatal("opaque process identity authorized network")
	}
	if !(GrantScope{Kind: ResourceDestination, ID: "https://example.test"}).Contains(GrantScope{Kind: ResourceDestination, ID: "https://example.test/mcp?x=1"}) {
		t.Fatal("MCP changed existing HTTP URL containment")
	}
}
