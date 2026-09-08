package skill

// nocx-aesm2: one previewed-document slot, two callers. The slot was right
// while Settings was the only acquisition surface (design §9 holds ONE source,
// visibly, and it can be taken back). skills.install (nocx-ojfuc.1) made the
// assistant a second caller, and two concurrent runs are two callers even
// after nocx-ojfuc.4 removed the paste box — so the loser's install refused
// with "nothing has been read from that address", naming a cause that did not
// happen, about an address the person HAD read.

import (
	"context"
	"strings"
	"testing"
)

const secondInstallableDocument = "---\nname: release\ndescription: Cut a release\n---\n" +
	"Run the release script.\n"

func TestPreview_AnotherPreviewDoesNotForgetTheFirst(t *testing.T) {
	stand := newInstallStand(t, installableDocument)
	other := serveDocument(t, "", secondInstallableDocument)

	if _, err := stand.store.Preview(context.Background(), stand.server.url); err != nil {
		t.Fatalf("preview the first: %v", err)
	}
	if _, err := stand.store.Preview(context.Background(), other.URL); err != nil {
		t.Fatalf("preview the second: %v", err)
	}

	// The person approves the FIRST one. Nothing about the second address
	// makes the bytes they read stop being the bytes they read.
	if _, err := stand.store.Install(context.Background(), stand.server.url); err != nil {
		t.Fatalf("install the first after previewing a second: %v", err)
	}
	if _, ok := stand.installed(t, "deploy"); !ok {
		t.Fatal("the first skill was not installed")
	}
}

func TestPreview_EachApprovalIsSpentOnItsOwn(t *testing.T) {
	stand := newInstallStand(t, installableDocument)
	other := serveDocument(t, "", secondInstallableDocument)

	for _, address := range []string{stand.server.url, other.URL} {
		if _, err := stand.store.Preview(context.Background(), address); err != nil {
			t.Fatalf("preview %s: %v", address, err)
		}
	}
	if _, err := stand.store.Install(context.Background(), stand.server.url); err != nil {
		t.Fatalf("install the first: %v", err)
	}
	// Spending the first leaves the second spendable, and spending the first
	// twice is still refused.
	if _, err := stand.store.Install(context.Background(), other.URL); err != nil {
		t.Fatalf("install the second: %v", err)
	}
	if _, err := stand.store.Install(context.Background(), stand.server.url); err == nil {
		t.Fatal("want a refusal: the first approval was already spent")
	}
}

func TestPreview_DisplacedByTheBoundSaysSoRatherThanDenyingTheRead(t *testing.T) {
	stand := newInstallStand(t, installableDocument)

	// One more preview than the store keeps. Each address serves the same
	// document; only the address differs, which is all the key is.
	addresses := make([]string, 0, maxRememberedPreviews+1)
	addresses = append(addresses, stand.server.url)
	for i := 0; i < maxRememberedPreviews; i++ {
		addresses = append(addresses, serveDocument(t, "", installableDocument).URL)
	}
	for _, address := range addresses {
		if _, err := stand.store.Preview(context.Background(), address); err != nil {
			t.Fatalf("preview %s: %v", address, err)
		}
	}

	_, err := stand.store.Install(context.Background(), addresses[0])
	if err == nil {
		t.Fatal("want a refusal: the oldest preview was displaced")
	}
	// THE POINT OF THE BEAD. The refusal may not claim the address was never
	// read, because it was.
	if strings.Contains(err.Error(), "nothing has been read from that address") {
		t.Fatalf("the refusal denies a read that happened: %v", err)
	}
	if !strings.Contains(err.Error(), "displaced") {
		t.Fatalf("the refusal does not name the cause that happened: %v", err)
	}
	// The newest survives, so the bound evicts the oldest and not the newest.
	if _, err := stand.store.Install(context.Background(), addresses[len(addresses)-1]); err != nil {
		t.Fatalf("install the newest: %v", err)
	}
}
