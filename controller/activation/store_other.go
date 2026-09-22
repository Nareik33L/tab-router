//go:build !darwin

package activation

func Open(dataDir string) SecretStore {
	return FileStore{Path: FilePath(dataDir)}
}
