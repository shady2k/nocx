package helperfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/transfer"
)

var _ filesystem.Downloader = (*Provider)(nil)

func (p *Provider) Source() transfer.Source {
	return transfer.NewSource(downloadFS{p}, transfer.DefaultChunk)
}

type downloadFS struct{ p *Provider }

// The streaming interface is context-free; do not leave a download's Open or
// a chunk waiting forever on a nonresponsive peer.
const downloadCallTimeout = 20 * time.Second

func (f downloadFS) Open(name string) (transfer.RemoteReader, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadCallTimeout)
	defer cancel()
	stat, err := f.p.stat(ctx, name)
	if err != nil {
		return nil, 0, downloadError(err)
	}
	if stat.Kind != filesystem.KindRegular {
		return nil, 0, &filesystem.ErrNotRegular{Path: name, Kind: stat.Kind, Err: transfer.ErrNotRegular}
	}
	if stat.Identity == "" {
		return nil, 0, &filesystem.ErrChangedDuringRead{Path: name}
	}
	return &downloadReader{p: f.p, name: name, size: stat.Size, identity: stat.Identity}, stat.Size, nil
}

type downloadReader struct {
	p            *Provider
	name         string
	size, offset int64
	identity     string
}

func (r *downloadReader) Read(dst []byte) (int, error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}
	if len(dst) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), downloadCallTimeout)
	defer cancel()
	length := min(r.size-r.offset, int64(len(dst)))
	out, err := r.p.readRange(ctx, r.name, r.offset, length)
	if err != nil {
		return 0, downloadError(err)
	}
	if out.Identity != r.identity || int64(len(out.Data)) > length {
		return 0, &filesystem.ErrChangedDuringRead{Path: r.name}
	}
	if len(out.Data) == 0 {
		return 0, io.EOF
	}
	n := copy(dst, out.Data)
	r.offset += int64(n)
	return n, nil
}

// A range read has no host-side descriptor to close.
func (*downloadReader) Close() error { return nil }

func downloadError(err error) error {
	var unavailable *filesystem.ErrUnavailable
	if errors.As(err, &unavailable) {
		return errors.Join(err, fs.ErrClosed)
	}
	return err
}
