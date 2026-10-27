package constants

import "io/fs"

const (
	RuntimeConnectionFilename = "runtime-connection.json"

	// A 32-byte token provides 256 bits of OS-generated entropy. Raw URL-safe
	// base64 encodes it as 43 characters without padding.
	SessionTokenBytes         = 32
	SessionTokenEncodedLength = 43
	BearerPrefixLength        = len("Bearer ")

	// The connection document contains a bearer credential, so POSIX systems
	// restrict both its directory and file to the current user.
	ConnectionDirectoryMode fs.FileMode = 0o700
	ConnectionFileMode      fs.FileMode = 0o600
	MaxConnectionFileBytes              = 1024
)
