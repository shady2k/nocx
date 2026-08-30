package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
)

// FileVersionStrategy records which evidence binds an approved path. Stat is
// cheap and catches accidental replacement; the content hash also catches an
// intentional rewrite. In one sentence: stat catches accidents, hash catches intent.
type FileVersionStrategy string

const (
	FileVersionStat FileVersionStrategy = "stat"
	FileVersionHash FileVersionStrategy = "sha256"
)

// FileVersionPolicy chooses when a path gets content evidence in addition to
// its device, inode, size and ctime. Executables are always hashed, and small
// files are cheap to hash; larger ordinary files use the stat identity only.
type FileVersionPolicy struct {
	HashExecutable bool
	HashFilesUpTo  int64
}

const defaultHashFilesUpTo int64 = 64 * 1024

func DefaultFileVersionPolicy() FileVersionPolicy {
	return FileVersionPolicy{HashExecutable: true, HashFilesUpTo: defaultHashFilesUpTo}
}

// FileVersionSource is the narrow filesystem seam used by the version check.
// ReadFile is needed only for hash-bound versions; stat-bound versions never
// read file contents.
type FileVersionSource interface {
	Stat(string) (os.FileInfo, error)
	ReadFile(string) ([]byte, error)
}

type osFileVersionSource struct{}

func (osFileVersionSource) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

func (osFileVersionSource) ReadFile(path string) ([]byte, error) {
	// #nosec G304 -- the path is the approved file selected by the caller.
	return os.ReadFile(path)
}

// FileVersion is the path identity recorded with an approval. Device, Inode,
// Size and ctime are always present. SHA256 is present only for hash-bound
// versions and is computed over the exact bytes read or supplied by a writer.
type FileVersion struct {
	Path      string
	Device    uint64
	Inode     uint64
	Size      int64
	CTimeSec  int64
	CTimeNsec int64
	Strategy  FileVersionStrategy
	SHA256    string
}

// FileVersionMismatchError means the approved path no longer names the same
// version. Path is deliberately part of the error so a refusal identifies the
// file the person must reconsider.
type FileVersionMismatchError struct {
	Path   string
	Reason string
}

func (e *FileVersionMismatchError) Error() string {
	return fmt.Sprintf("file %q changed since approval: %s", e.Path, e.Reason)
}

// CaptureFileVersion captures the default path binding used for approvals.
func CaptureFileVersion(path string) (FileVersion, error) {
	return CaptureFileVersionFrom(path, osFileVersionSource{}, DefaultFileVersionPolicy())
}

// CaptureFileVersionFrom captures one path through source. Hash captures are
// bounded by a stat-before/read/stat-after fence: a replacement or concurrent
// mutation during the read is refused rather than binding torn bytes.
func CaptureFileVersionFrom(path string, source FileVersionSource, policy FileVersionPolicy) (FileVersion, error) {
	if source == nil {
		return FileVersion{}, fmt.Errorf("file version %q: nil source", path)
	}
	before, err := source.Stat(path)
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: stat: %w", path, err)
	}
	version, err := versionFromInfo(path, before)
	if err != nil {
		return FileVersion{}, err
	}
	if !shouldHash(before, policy) {
		version.Strategy = FileVersionStat
		return version, nil
	}

	data, err := source.ReadFile(path)
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: read for hash: %w", path, err)
	}
	after, err := source.Stat(path)
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: stat after hash: %w", path, err)
	}
	if !sameInfo(before, after) || int64(len(data)) != before.Size() {
		return FileVersion{}, &FileVersionMismatchError{Path: path, Reason: "changed while its content was being hashed"}
	}
	version.Strategy = FileVersionHash
	version.SHA256 = hashBytes(data)
	return version, nil
}

// CaptureWrittenFileVersion binds bytes already held by a writer. It performs
// one stat and never reads the path again; the supplied bytes are the content
// evidence for a hash-bound small or executable file.
func CaptureWrittenFileVersion(path string, data []byte) (FileVersion, error) {
	return CaptureWrittenFileVersionFrom(path, data, osFileVersionSource{})
}

func CaptureWrittenFileVersionFrom(path string, data []byte, source FileVersionSource) (FileVersion, error) {
	if source == nil {
		return FileVersion{}, fmt.Errorf("file version %q: nil source", path)
	}
	info, err := source.Stat(path)
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: stat written file: %w", path, err)
	}
	version, err := versionFromInfo(path, info)
	if err != nil {
		return FileVersion{}, err
	}
	version.Strategy = FileVersionHash
	version.SHA256 = hashBytes(data)
	return version, nil
}

// VerifyFileVersion checks the path immediately before dispatch. For a
// hash-bound version, the evidence interval begins at the first stat and ends
// at the second stat after the content read; a stat-bound version is one
// metadata sample. This proves only that those observations matched, not that
// a later consumer open or execution uses the same version.
//
// Narrow in-process tools could use the stronger alternative: open an *os.File
// and keep that descriptor through the operation. This shared path-based API
// does not choose it because current file capabilities pass paths, while
// session.run gives a command string to a shell that opens the path itself.
func VerifyFileVersion(version FileVersion) error {
	return VerifyFileVersionFrom(version, osFileVersionSource{})
}

func VerifyFileVersionFrom(version FileVersion, source FileVersionSource) error {
	if source == nil {
		return fmt.Errorf("file version %q: nil source", version.Path)
	}
	before, err := source.Stat(version.Path)
	if err != nil {
		return fmt.Errorf("file version %q: stat before execution: %w", version.Path, err)
	}
	compareErr := compareInfo(version, before)
	if compareErr != nil {
		return compareErr
	}
	if version.Strategy != FileVersionHash {
		return nil
	}
	data, err := source.ReadFile(version.Path)
	if err != nil {
		return fmt.Errorf("file version %q: read for verification: %w", version.Path, err)
	}
	after, err := source.Stat(version.Path)
	if err != nil {
		return fmt.Errorf("file version %q: stat after verification: %w", version.Path, err)
	}
	if !sameInfo(before, after) || int64(len(data)) != before.Size() {
		return &FileVersionMismatchError{Path: version.Path, Reason: "changed while its content was being verified"}
	}
	if hashBytes(data) != version.SHA256 {
		return &FileVersionMismatchError{Path: version.Path, Reason: "content hash differs"}
	}
	return nil
}

func versionFromInfo(path string, info os.FileInfo) (FileVersion, error) {
	if info == nil {
		return FileVersion{}, fmt.Errorf("file version %q: stat returned no file info", path)
	}
	if !info.Mode().IsRegular() {
		return FileVersion{}, fmt.Errorf("file version %q: not a regular file", path)
	}
	device, inode, ctimeSec, ctimeNsec, err := statIdentity(info)
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: stat identity: %w", path, err)
	}
	return FileVersion{Path: path, Device: device, Inode: inode, Size: info.Size(), CTimeSec: ctimeSec, CTimeNsec: ctimeNsec}, nil
}

func shouldHash(info os.FileInfo, policy FileVersionPolicy) bool {
	return (policy.HashExecutable && info.Mode()&0o111 != 0) || (policy.HashFilesUpTo >= 0 && info.Size() <= policy.HashFilesUpTo)
}

func compareInfo(version FileVersion, info os.FileInfo) error {
	current, err := versionFromInfo(version.Path, info)
	if err != nil {
		return err
	}
	if !sameVersionMetadata(version, current) {
		return &FileVersionMismatchError{Path: version.Path, Reason: "device, inode, size or ctime differs"}
	}
	return nil
}

func sameInfo(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return false
	}
	av, errA := versionFromInfo("", a)
	bv, errB := versionFromInfo("", b)
	return errA == nil && errB == nil && sameVersionMetadata(av, bv)
}

func sameVersionMetadata(a, b FileVersion) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.Size == b.Size && a.CTimeSec == b.CTimeSec && a.CTimeNsec == b.CTimeNsec
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func statIdentity(info os.FileInfo) (uint64, uint64, int64, int64, error) {
	if info == nil || info.Sys() == nil {
		return 0, 0, 0, 0, errors.New("missing platform stat data")
	}
	value := reflect.ValueOf(info.Sys())
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, 0, 0, 0, errors.New("missing platform stat data")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, 0, 0, 0, errors.New("invalid platform stat data")
	}
	device, ok := integerField(value, "Dev", "Device")
	if !ok {
		return 0, 0, 0, 0, errors.New("device is unavailable")
	}
	inode, ok := integerField(value, "Ino", "Inode", "FileIndex")
	if !ok {
		return 0, 0, 0, 0, errors.New("inode is unavailable")
	}
	ctime, ok := timeField(value, "Ctim", "Ctimespec", "Ctime", "ChangeTime")
	if !ok {
		return 0, 0, 0, 0, errors.New("ctime is unavailable")
	}
	return device, inode, ctime.sec, ctime.nsec, nil
}

type statTime struct {
	sec  int64
	nsec int64
}

func timeField(value reflect.Value, names ...string) (statTime, bool) {
	for _, name := range names {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		if sec, ok := signedTimeField(field, "Sec", "TV_sec", "Seconds"); ok {
			nsec, _ := signedTimeField(field, "Nsec", "TV_nsec", "Nanoseconds")
			return statTime{sec: sec, nsec: nsec}, true
		}
		if sec, ok := signedField(field); ok {
			return statTime{sec: sec}, true
		}
	}
	return statTime{}, false
}

func signedTimeField(value reflect.Value, names ...string) (int64, bool) {
	for _, name := range names {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		if result, ok := signedField(field); ok {
			return result, true
		}
		if result, ok := unsignedField(field); ok && result <= uint64(1<<63-1) {
			return int64(result), true
		}
	}
	return 0, false
}

func integerField(value reflect.Value, names ...string) (uint64, bool) {
	for _, name := range names {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		if result, ok := unsignedField(field); ok {
			return result, true
		}
		if result, ok := signedField(field); ok && result >= 0 {
			return uint64(result), true
		}
	}
	return 0, false
}

func unsignedField(value reflect.Value) (uint64, bool) {
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	default:
		return 0, false
	}
}

func signedField(value reflect.Value) (int64, bool) {
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	default:
		return 0, false
	}
}

func cloneFileVersions(versions []FileVersion) []FileVersion {
	if versions == nil {
		return nil
	}
	return append([]FileVersion(nil), versions...)
}
