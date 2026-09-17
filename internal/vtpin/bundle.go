package vtpin

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The headers travel as ONE file per target rather than as a list of GitHub
// release assets, because a release asset is a file: thirty-five headers per
// target would be thirty-five × six downloads and thirty-five × six names in
// the manifest for one ABI's worth of text.
//
// The bundle is deterministic by construction and that is not decoration: its
// sha256 is in the manifest, the fetch refuses a bundle that does not match,
// and the recipe's own output is what gets published. Timestamps, owners,
// names and ordering are all fixed here — Go's archive/tar and compress/gzip
// are the same code on every platform, which a `tar | gzip` pipeline is not
// (GNU and BSD disagree on all four).
const (
	bundleMode = 0o644
)

// bundleEpoch is the fixed mtime every entry carries. Zero is what the format
// allows and what makes two packs of the same tree byte-equal.
var bundleEpoch = time.Unix(0, 0).UTC()

// PackHeaders writes dir — with its own base name as the prefix — to out as a
// deterministic tar.gz. The prefix is what makes the bundle unpack into an
// include/ directory: a bundle of .../include/ghostty unpacks to ghostty/vt.h.
func PackHeaders(dir, out string) error {
	prefix := filepath.Base(filepath.Clean(dir))
	entries, err := collect(dir, prefix)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("pack %s: no files under %s", out, dir)
	}
	file, err := os.Create(out)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		body, err := os.Open(e.abs)
		if err != nil {
			return closeAll(file, gz, tw, err)
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     e.name,
			Mode:     bundleMode,
			Size:     e.size,
			ModTime:  bundleEpoch,
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = body.Close()
			return closeAll(file, gz, tw, err)
		}
		if _, err := io.Copy(tw, body); err != nil {
			_ = body.Close()
			return closeAll(file, gz, tw, err)
		}
		if err := body.Close(); err != nil {
			return closeAll(file, gz, tw, err)
		}
	}
	if err := tw.Close(); err != nil {
		return closeAll(file, gz, tw, err)
	}
	if err := gz.Close(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// closeAll unwinds a half-written bundle. Every error here is subordinated to
// the cause: the caller is already returning a failure, and a close error
// reported in its place would hide what actually went wrong.
func closeAll(file *os.File, gz *gzip.Writer, tw *tar.Writer, cause error) error {
	_ = tw.Close()
	_ = gz.Close()
	_ = file.Close()
	_ = os.Remove(file.Name())
	return cause
}

// maxBundleBytes bounds what may be extracted from one headers bundle. The
// real bundle is ~250 KB; the limit is what keeps a substituted — or simply
// hostile — archive from filling a disk, since extraction is the one place
// this package decompresses something a network delivered. The fetch verifies
// the bundle's sha256 first, and this is the belt to that pair of braces.
const maxBundleBytes int64 = 64 << 20

type entry struct {
	abs  string
	name string
	size int64
}

// collect lists the regular files under dir in a stable order, with slash
// separators and the prefix applied. Symlinks are refused rather than
// followed: a bundle is meant to be the headers the build installed, and a
// link would make its bytes somebody else's.
func collect(dir, prefix string) ([]entry, error) {
	var out []entry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; a header bundle is copied, not linked", p)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is neither a directory nor a regular file", p)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, entry{
			abs:  p,
			name: path.Join(prefix, filepath.ToSlash(rel)),
			size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// UnpackHeaders extracts a bundle into dest, which is created. Anything that
// would write outside dest is refused: the bundle comes from a downloaded
// file, and a download is exactly the input a traversal is attempted through.
func UnpackHeaders(bundle, dest string) error {
	file, err := os.Open(bundle) //nolint:gosec // the bundle is the file the manifest names and the fetch just verified
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	var extracted int64
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		extracted += hdr.Size
		if extracted > maxBundleBytes {
			return fmt.Errorf("%s: bundle holds more than %d bytes", bundle, maxBundleBytes)
		}
		if err := writeEntry(dest, hdr, tr); err != nil {
			return fmt.Errorf("%s: %w", bundle, err)
		}
	}
}

// writeEntry writes one regular file of a bundle, and refuses a name that would
// land outside dest. It is separate from the loop so the traversal check and
// the write live together, as the pair they are.
func writeEntry(dest string, hdr *tar.Header, body io.Reader) error {
	name := path.Clean(hdr.Name)
	if name == "." || path.IsAbs(name) || strings.HasPrefix(name, "../") {
		return fmt.Errorf("entry %q escapes the bundle", hdr.Name)
	}
	if hdr.Typeflag != tar.TypeReg {
		return fmt.Errorf("entry %q is not a regular file", hdr.Name)
	}
	target := filepath.Join(dest, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	out, err := os.Create(target) //nolint:gosec // the name is this function's subject and was checked against traversal just above
	if err != nil {
		return err
	}
	// CopyN, not Copy: a tar entry is exactly hdr.Size bytes, so the count is
	// the entry's own bound and a bundle that decompresses to more than it
	// declares cannot run away with the disk.
	if _, err := io.CopyN(out, body, hdr.Size); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
