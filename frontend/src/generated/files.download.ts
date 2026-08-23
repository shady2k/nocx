/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.download.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the files.download JSON-RPC method — the call that takes a file OFF the machine the active tab is on. It is files.upload's mirror, and two of that method's rules carry over unchanged while one deliberately does not. R1 carries over: a download is addressed by a bindingId and authorised by the registry re-checking that the binding's session belongs to the requesting connection, because reading a file off the WRONG host is as wrong as writing to it, and a binding names one session's filesystem and nothing else. D8 carries over: the binding's use-guard covers this synchronous call and is dropped before the transfer runs, so closing the tab never waits for a 400 MB file. R2 does NOT carry over, and the asymmetry is correct rather than an oversight — files.upload has no sourcePath because a path on the BACKEND's disk is scoped by nothing, whereas a download's path is scoped by the binding it is addressed through, which the caller can already enumerate with files.list and read with files.read. Naming it is the same authority in a different bound, not new authority, so there is no source ticket here and inventing one would be a check that cannot fail. Unlike files.upload the result is a single shape and not a union: there is no collision question to ask, because nothing on the far host is being replaced.
 */
export interface FilesDownloadResult {
  /**
   * The transfer's opaque id, 32 lowercase hex characters. Progress and the terminal outcome arrive as files.downloadProgress and files.downloadDone addressed by this id, and files.downloadCancel takes it. It is not a credential: cancelling by it still re-checks that the caller owns the transfer's session, and it names a download only — a transfer id from the other direction is refused here rather than honoured.
   */
  transferId: string
  /**
   * The one-shot ticket authorising the fetch that carries the bytes, 64 lowercase hex characters from crypto/rand. The width is a pattern and not only a sentence, because a sentence cannot refuse anything. It is a BEARER credential and the design says so out loud: a GET cannot present the WebSocket subprotocol that guards /session, so possession of this ticket is what authorises reading those bytes. The violation it protects against is not the sink ticket's — a stolen sink ticket puts attacker-chosen content at a path, which is an integrity violation, while a stolen download ticket reads a file off somebody else's server, which is a confidentiality one. It therefore never appears in a log line or an error string, it travels in the request path only because the request never leaves loopback, and it is claimed exactly once: a second fetch while the first is still running is refused, and one after the transfer has ended finds nothing. Presenting it at the upload route finds nothing either — a ticket on the wrong route names nothing at all.
   */
  ticket: string
  /**
   * Where to GET the bytes — the path on the backend's own HTTP surface, of the form /download/{ticket}, constrained by a pattern so a result naming some other URL is refused rather than read past. The bytes travel over HTTP rather than on the WebSocket for the upload's three reasons with every term stronger in this direction: they run the SAME way as bulk PTY output, so they would queue in front of the frames a person is reading; the outbound queue is bounded and deliberately lossy, and a file's bytes may not be dropped where terminal output may; and a browser streams an HTTP response to disk by itself, where a page receiving WebSocket messages would have to hold the whole file in the renderer's heap before any of it reached the disk. The response is framed at exactly the size below, so a transfer that fails part-way arrives as a body short of its own declared length, which every HTTP client already treats as the broken transfer it is.
   */
  url: string
  /**
   * The file's base name — what it is called when it lands. Never a path: the person asked for one file and the directory it came from is already on their screen. It reaches the fetch as Content-Disposition, sanitised there, because a POSIX file name may contain anything but '/' and NUL and a header is not a place to find out.
   */
  name: string
  /**
   * The file's length in bytes, and it is authoritative rather than advisory. It was measured by an fstat on the OPEN handle the backend is holding for this transfer, not on the name: between this answer and the fetch that redeems it the name can be renamed, replaced, or be a symlink whose target moved, and a size taken from the name would then describe different bytes from the ones sent. The open handle pins the object, so the number and the bytes are the same file by construction — which is what makes it safe to declare as the fetch's Content-Length. Zero is legitimate; an empty file is a file.
   */
  size: number
}
