//go:build !enterprise

package vault

// managedKeyRegistrySubPath is the storage prefix used by the registry.
// We need to define the constant even though managed keys is a Vault Enterprise
// feature in order to set up seal wrapping in the SystemBackend.
const managedKeyRegistrySubPath = "managed-key-registry/"

func (c *Core) setupManagedKeyRegistry() error {
	return nil
}
