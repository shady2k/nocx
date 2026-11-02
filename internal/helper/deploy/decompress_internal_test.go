package deploy

// The payload cache (nocx-atpw5.1.17): one decompression per artifact per
// process, and never a cached answer for an artifact this process has not been
// handed. The assertions are on the SHARED bytes — an install writes them out
// and never modifies them, so reuse is the same slice and a recomputation is a
// different one.

import (
	"bytes"
	"compress/gzip"
	"sync"
	"testing"
)

func compressedArtifact(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(payload)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// A second install of one artifact reads the payload the first one
// decompressed instead of spending the decompression again — which is what a
// stand that starts an App, and an App that installs its helper on a second
// host, both asked for.
func TestAnArtifactIsDecompressedOncePerProcess(t *testing.T) {
	compressed := compressedArtifact(t, "the same helper twice\n")
	first, err := decompressedPayload(compressed)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	second, err := decompressedPayload(compressed)
	if err != nil {
		t.Fatalf("decompress again: %v", err)
	}
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("the artifact decompressed to nothing, so reuse would be vacuous")
	}
	if &first[0] != &second[0] {
		t.Fatal("the second call decompressed the artifact again: it did not read the payload this process already holds")
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("the reused payload differs from the first: %q != %q", first, second)
	}
}

// A DIFFERENT artifact is not served the cached payload. The bytes, not the
// length or the name, decide: these two decompress to different content, and a
// cache keyed on anything but the input would answer one for the other.
func TestADifferentArtifactIsNotServedFromTheCache(t *testing.T) {
	one, err := decompressedPayload(compressedArtifact(t, "helper one\n"))
	if err != nil {
		t.Fatalf("decompress the first artifact: %v", err)
	}
	two, err := decompressedPayload(compressedArtifact(t, "helper two\n"))
	if err != nil {
		t.Fatalf("decompress the second artifact: %v", err)
	}
	if bytes.Equal(one, two) {
		t.Fatalf("both artifacts decompressed to %q, so this test cannot tell them apart", one)
	}
	if &one[0] == &two[0] {
		t.Fatal("the second artifact was answered from the first one's cached payload")
	}
}

// An artifact that cannot be decompressed is reported, never remembered under
// its own digest, and the good artifact that follows it still installs.
func TestAFailedDecompressionIsNotRemembered(t *testing.T) {
	broken := []byte("not gzip at all")
	if _, err := decompressedPayload(broken); err == nil {
		t.Fatal("a non-gzip artifact decompressed, so this test proves nothing")
	}
	if _, err := decompressedPayload(broken); err == nil {
		t.Fatal("the second call reported success for the same broken artifact")
	}
	good, err := decompressedPayload(compressedArtifact(t, "a real artifact\n"))
	if err != nil {
		t.Fatalf("decompress a good artifact after a failed one: %v", err)
	}
	if !bytes.Equal(good, []byte("a real artifact\n")) {
		t.Fatalf("the good artifact decompressed to %q", good)
	}
}

// Concurrent installs of one artifact read one payload, and the race detector
// is what checks the map: this test's assertion is that they agree.
func TestConcurrentInstallsShareOnePayload(t *testing.T) {
	compressed := compressedArtifact(t, "a helper many panes install at once\n")
	var wg sync.WaitGroup
	payloads := make([][]byte, 8)
	for i := range payloads {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := decompressedPayload(compressed)
			if err != nil {
				t.Errorf("decompress %d: %v", i, err)
				return
			}
			payloads[i] = p
		}(i)
	}
	wg.Wait()
	for i, p := range payloads {
		if !bytes.Equal(p, []byte("a helper many panes install at once\n")) {
			t.Fatalf("caller %d read %q", i, p)
		}
		if &p[0] != &payloads[0][0] {
			t.Fatalf("caller %d decompressed its own copy rather than reading the shared payload", i)
		}
	}
}
