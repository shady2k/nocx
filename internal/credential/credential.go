// Package credential provides the Secret type and the SecretStore capability.
//
// It deliberately no longer contains a store implementation. Secrets are held
// by providers under internal/vault, and the Vault is what the composition
// root wires — see ADR-0011 as amended by the vault design.
package credential
