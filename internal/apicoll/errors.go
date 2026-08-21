package apicoll

import "errors"

// Domain errors for the collection folder. Each is a sentinel a caller can
// switch on, because every one of them is a distinct answer the surface has
// to give: "that folder is not a collection" is not "that folder is empty",
// and "you may not look there" is not "there is nothing there".

// ErrPathOutsideCollection — the path names something the collection does not
// own: it is absolute, it contains `..`, it is not already clean, or a
// component of it is a symlink. All four are one answer because they are one
// attack: a collection arriving in a pull request reaching a file outside
// itself (§13.1).
//
// Refused, never clamped. A silently rewritten path is a surface that reports
// success for something it did not do.
var ErrPathOutsideCollection = errors.New("apicoll: path is outside the collection")

// ErrNotARequestPath — the path is inside the collection but does not name a
// request file: it is not a `.json` file, it is the manifest, it is under
// `environments/`, or it is not a regular file. Without this, WriteRequest is
// a way to overwrite the manifest through the request surface.
var ErrNotARequestPath = errors.New("apicoll: path does not name a request file")

// ErrNoManifest — the folder carries no collection manifest, so it is not a
// collection. This is deliberately not an empty collection: a caller that saw
// one would offer to write into a folder nobody chose for this.
var ErrNoManifest = errors.New("apicoll: folder has no collection manifest")

// ErrUnknownHandle — the handle was never minted, or belongs to a service
// that has gone. A handle is not a bearer token for a path; it is a row in a
// table this package owns.
var ErrUnknownHandle = errors.New("apicoll: unknown collection handle")

// ErrRootChanged — the folder that was opened is no longer the folder at that
// path (§13.1, fourth rule). The handle is re-validated per operation rather
// than trusted from open time, and a replaced root is reported rather than
// served out of whatever now sits there.
var ErrRootChanged = errors.New("apicoll: the collection folder has been replaced or removed since it was opened")

// ErrRequestNotFound — nothing at that path inside the collection.
var ErrRequestNotFound = errors.New("apicoll: no such request in the collection")

// ErrMalformedRequest — the file exists and is not a request. It is named, so
// that one bad file is a bad file rather than a collection that will not open.
var ErrMalformedRequest = errors.New("apicoll: request file is malformed")

// ErrInvalidCollectionName — the name given for a new collection is not a
// single folder name: it is empty, it is a path, it is `.` or `..`, it
// starts with a dot, or it is longer than a filesystem component allows.
//
// A sentinel because it is the CALLER's error and a surface has to be able
// to say so: the remedy is to name something else, which is a different
// sentence from "that folder is already a collection" and from anything the
// filesystem might have refused. Refused, never sanitised — a name quietly
// stripped of its slashes creates a folder the user did not ask for under a
// name they did not choose.
var ErrInvalidCollectionName = errors.New("apicoll: that is not a collection name")

// ErrCollectionExists — NewDefaultCollection was asked for a name that
// already has a folder. Creating it again would write a fresh manifest over
// somebody's collection, which is data loss wearing the word "create".
var ErrCollectionExists = errors.New("apicoll: a collection with that name already exists")
