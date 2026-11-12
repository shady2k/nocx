//go:build darwin

package endpoint_test

import (
	"bytes"
	"debug/macho"
	"testing"
)

func TestHelperCarriesItsOwnLocalNetworkUsageDescription(t *testing.T) {
	bin, _ := helperBinary(t)
	image, err := macho.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = image.Close() }()
	section := image.Section("__info_plist")
	if section == nil {
		t.Fatal("bare helper has no embedded Info.plist for its own Local Network prompt")
	}
	data, err := section.Data()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"NSLocalNetworkUsageDescription", "dev.nocx.helper", "nocx-helper connects your terminal sessions"} {
		if !bytes.Contains(data, []byte(value)) {
			t.Fatalf("helper Info.plist lacks %q: %s", value, data)
		}
	}
}
