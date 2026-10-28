//go:build !linux

package apexv1

// observer observes nothing off Linux: there is no /proc to read a
// check's sockets from, so no Observed is recorded and no
// check.tag-mismatch notice is given.
type observer struct{}

func startObserver(*checkGroup) *observer { return nil }

func (o *observer) finish() *Observed { return nil }
