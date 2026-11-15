//go:build darwin && nocx_local_ssh

package notices

import _ "embed"

// Copied verbatim from github.com/ebitengine/purego v0.9.1, LICENSE.
// Origin: https://github.com/ebitengine/purego/blob/v0.9.1/LICENSE
// SHA256: b40930bbcf80744c86c46a12bc9da056641d722716c378f5659b9e555ef833e1
//
//go:embed purego-LICENSE.txt
var puregoLicense string

func init() {
	supplementaryNotices = "\n\n" +
		"github.com/ebitengine/purego v0.9.1 — Apache-2.0\n" +
		"Origin: https://github.com/ebitengine/purego/blob/v0.9.1/LICENSE\n" +
		"SHA256: b40930bbcf80744c86c46a12bc9da056641d722716c378f5659b9e555ef833e1\n\n" + puregoLicense
}
