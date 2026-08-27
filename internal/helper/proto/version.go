package proto

// Version is the protocol version the helper and the backend must agree on.
// A hello carrying another version is refused with exit code 42 before the
// helper writes anything to stdout (D5).
const Version = "1"
