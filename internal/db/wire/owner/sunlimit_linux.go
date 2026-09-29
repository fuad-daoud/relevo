//go:build linux

package owner

// sunPathLimit is sizeof(sun_path) in the kernel's sockaddr_un. The path must
// be strictly shorter: the kernel stores a terminating NUL in the same field.
const sunPathLimit = 108
