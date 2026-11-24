//go:build !unix

package assets

import "os"

const sourceOpenFlags = os.O_RDONLY
