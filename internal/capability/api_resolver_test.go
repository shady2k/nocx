package capability

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

const resolverTestReference = "{{secret:secrow:test}}"

type resolverTestRefs struct {
	calls int
	value func(call int) string
}

func (r *resolverTestRefs) ResolveText(_ context.Context, text string) (string, []PlacedSecret, error) {
	if !strings.Contains(text, "{{secret:") {
		return text, nil, nil
	}
	r.calls++
	if !strings.Contains(text, resolverTestReference) {
		return "", nil, &UnresolvedSecretError{Reference: text}
	}
	value := r.value(r.calls)
	return strings.ReplaceAll(text, resolverTestReference, value), []PlacedSecret{{Name: "secrow:test", Value: value}}, nil
}

func TestResolveRequestSecretsCoversEverySendableFieldAndBodyKind(t *testing.T) {
	cases := []struct {
		name string
		set  func(*apicoll.Request, string)
		get  func(apicoll.Request) string
	}{
		{"url", func(r *apicoll.Request, value string) { r.URL = value }, func(r apicoll.Request) string { return r.URL }},
		{"query name", func(r *apicoll.Request, value string) {
			r.Query = []apicoll.Param{{Name: value, Value: "plain", Enabled: true}}
		}, func(r apicoll.Request) string { return r.Query[0].Name }},
		{"query value", func(r *apicoll.Request, value string) {
			r.Query = []apicoll.Param{{Name: "plain", Value: value, Enabled: true}}
		}, func(r apicoll.Request) string { return r.Query[0].Value }},
		{"header name", func(r *apicoll.Request, value string) {
			r.Headers = []apicoll.Header{{Name: value, Value: "plain", Enabled: true}}
		}, func(r apicoll.Request) string { return r.Headers[0].Name }},
		{"header value", func(r *apicoll.Request, value string) {
			r.Headers = []apicoll.Header{{Name: "X-Test", Value: value, Enabled: true}}
		}, func(r apicoll.Request) string { return r.Headers[0].Value }},
		{"bearer token", func(r *apicoll.Request, value string) { r.Auth = apicoll.Auth{Kind: apicoll.AuthBearer, Token: value} }, func(r apicoll.Request) string { return r.Auth.Token }},
		{"basic user", func(r *apicoll.Request, value string) { r.Auth = apicoll.Auth{Kind: apicoll.AuthBasic, User: value} }, func(r apicoll.Request) string { return r.Auth.User }},
		{"basic password", func(r *apicoll.Request, value string) {
			r.Auth = apicoll.Auth{Kind: apicoll.AuthBasic, Password: value}
		}, func(r apicoll.Request) string { return r.Auth.Password }},
		{"api key token", func(r *apicoll.Request, value string) { r.Auth = apicoll.Auth{Kind: apicoll.AuthAPIKey, Token: value} }, func(r apicoll.Request) string { return r.Auth.Token }},
		{"raw body", func(r *apicoll.Request, value string) { r.Body = apicoll.Body{Kind: apicoll.BodyRaw, Text: value} }, func(r apicoll.Request) string { return r.Body.Text }},
		{"json body", func(r *apicoll.Request, value string) {
			r.Body = apicoll.Body{Kind: apicoll.BodyJSON, Text: `{"token":"` + value + `"}`}
		}, func(r apicoll.Request) string { return r.Body.Text }},
		{"form body", func(r *apicoll.Request, value string) {
			r.Body = apicoll.Body{Kind: apicoll.BodyForm, Text: "token=" + value}
		}, func(r apicoll.Request) string { return r.Body.Text }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs := &resolverTestRefs{value: func(int) string { return "resolved" }}
			request := apicoll.Request{}
			tc.set(&request, resolverTestReference)
			resolved, placed, err := (&apiCollectionService{refs: refs}).resolveRequestSecrets(context.Background(), request)
			if err != nil {
				t.Fatalf("resolveRequestSecrets: %v", err)
			}
			if got := tc.get(resolved); !strings.Contains(got, "resolved") {
				t.Fatalf("resolved field = %q, want resolved value", got)
			}
			if len(placed) != 1 || placed[0].Value != "resolved" {
				t.Fatalf("placed secrets = %+v, want one resolved value", placed)
			}

			request = apicoll.Request{}
			tc.set(&request, "{{secret:display name}}")
			_, _, err = (&apiCollectionService{refs: refs}).resolveRequestSecrets(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), "display name") {
				t.Fatalf("display-name error = %v, want named refusal", err)
			}
		})
	}
}

func TestResolveRequestSecretsKeepsDistinctValuesForOneHandle(t *testing.T) {
	refs := &resolverTestRefs{value: func(call int) string {
		if call == 1 {
			return "old-value"
		}
		return "new-value"
	}}
	request := apicoll.Request{
		URL:     resolverTestReference,
		Headers: []apicoll.Header{{Name: "X-Token", Value: resolverTestReference, Enabled: true}},
	}
	_, placed, err := (&apiCollectionService{refs: refs}).resolveRequestSecrets(context.Background(), request)
	if err != nil {
		t.Fatalf("resolveRequestSecrets: %v", err)
	}
	if len(placed) != 2 || placed[0].Value != "old-value" || placed[1].Value != "new-value" {
		t.Fatalf("placed secrets = %+v, want both values", placed)
	}
}

func TestResolveRequestSecretsFollowsEveryBodyKind(t *testing.T) {
	for _, tc := range []struct {
		kind      string
		transmits bool
	}{
		{kind: apicoll.BodyNone, transmits: false},
		{kind: apicoll.BodyRaw, transmits: true},
		{kind: apicoll.BodyJSON, transmits: true},
		{kind: apicoll.BodyForm, transmits: true},
		{kind: apicoll.BodyFile, transmits: false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			request := apicoll.Request{Body: apicoll.Body{Kind: tc.kind, Text: resolverTestReference}}
			resolved, _, err := (&apiCollectionService{
				refs: &resolverTestRefs{value: func(int) string { return "resolved" }},
			}).resolveRequestSecrets(context.Background(), request)
			if err != nil {
				t.Fatalf("resolveRequestSecrets: %v", err)
			}
			want := resolverTestReference
			if tc.transmits {
				want = "resolved"
			}
			if resolved.Body.Text != want {
				t.Fatalf("body text = %q, want %q", resolved.Body.Text, want)
			}
		})
	}
}
