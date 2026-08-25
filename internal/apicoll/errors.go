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

// ErrRequestExists — a move's destination already holds a request file of
// that name. Refused rather than overwritten, which is the rule every
// create on this surface follows, and it is THE one this package cannot
// paper over: the move is a rename, and a rename that replaced the
// destination would be the second copy of one file that the method exists
// to forbid.
var ErrRequestExists = errors.New("apicoll: a request with that name is already there")

// ErrMalformedRequest — the file exists and is not a request. It is named, so
// that one bad file is a bad file rather than a collection that will not open.
var ErrMalformedRequest = errors.New("apicoll: request file is malformed")

// ErrMalformedFolderVariables — the reserved folder-variable file exists but
// cannot be read as its strict variables document. A bad file is named rather
// than treated as an empty scope, because silently dropping it could send a
// request with a parent folder's value.
var ErrMalformedFolderVariables = errors.New("apicoll: folder variables file is malformed")

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

// ErrInvalidFolderName — the name given for a new folder inside a
// collection is not a single path component: it is empty, it is a path, it
// is `.` or `..`, it starts with a dot, or it is longer than a filesystem
// component allows. It is also what `environments` gets at the top of a
// collection, because that name is already taken by the environments
// directory (§6.2).
//
// A sentinel of its own beside ErrInvalidCollectionName, sharing one
// implementation (validateComponentName): the RULE is one rule and has one
// owner, but the SENTENCE a surface shows is not — "that is not a
// collection name" under a New Folder field would name the wrong thing.
var ErrInvalidFolderName = errors.New("apicoll: that is not a folder name")

// ErrFolderExists — a folder was asked for under a name something already
// occupies. Refused rather than merged, which is the rule the import
// already follows for its destination: a create that quietly adopted
// somebody's folder would report making a thing it found.
var ErrFolderExists = errors.New("apicoll: something with that name is already there")

// ErrFolderNotFound — the folder a new one was to be created inside is not
// in the collection, or is not a folder. Distinct from ErrRequestNotFound
// because it is a different question with a different remedy: the caller
// named a parent that is not there, and the move is to make it first.
var ErrFolderNotFound = errors.New("apicoll: no such folder in the collection")
