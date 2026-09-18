//go:build !windows

package systemproxy

type noopResolverBackend struct{}

func NewResolver() (*Resolver, error) {
	return newResolverWithBackend(noopResolverBackend{}), nil
}

func (noopResolverBackend) resolve(rawurl string, cfg Config) (string, error) {
	return "", nil
}

func (noopResolverBackend) close() error {
	return nil
}

func Discover() Config {
	return Config{}
}

func ResolveProxyForURL(rawurl string, cfg Config) (string, error) {
	return "", nil
}
