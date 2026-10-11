package sessionruntime

import (
	"net/url"
	"strings"
)

const osc7FilePrefix = "file://"

// decodeCwdReport decodes the OSC 7 URI already recognized by the emulator.
// It mirrors the old renderer's URI semantics; it does not parse terminal
// bytes or validate a path against the host filesystem.
func decodeCwdReport(body []byte) (CwdReport, bool) {
	payload := string(body)
	if !strings.HasPrefix(payload, osc7FilePrefix) {
		return CwdReport{}, false
	}
	uri := payload[len(osc7FilePrefix):]
	slash := strings.IndexByte(uri, '/')
	if slash < 0 {
		return CwdReport{}, false
	}
	host, err := url.PathUnescape(uri[:slash])
	if err != nil {
		return CwdReport{}, false
	}
	path, err := url.PathUnescape(uri[slash:])
	if err != nil {
		return CwdReport{}, false
	}
	return CwdReport{Host: host, Path: path}, true
}
