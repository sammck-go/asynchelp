// Package asyncobj provides tools that make it easier to build and manage objects that have asynchronous
// lifecycles. In particular, it provides a pattern for clean asynchronous activation and shutdown of objects with blocking resources
// that must be cleanly released.
package asyncobj

import (
	"github.com/sammck-go/logger"
)

// Logger is a convenient type alias for logger.Logger
type Logger logger.Logger
