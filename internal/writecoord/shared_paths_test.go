package writecoord

import "testing"

func TestSharedPathsQueueAndCancellation(t *testing.T) {
	var c Coordinator
	release := hold(t, &c, Scope{SharedPaths: []string{"/links/project"}})
	writer := startAttempt(t, &c, Scope{Paths: []string{"/links"}})
	reader := startAttempt(t, &c, Scope{SharedPaths: []string{"/links/project"}})
	assertQueued(t, &c, writer)
	assertQueued(t, &c, reader)
	startAttempt(t, &c, Scope{Paths: []string{"/other/file"}, SharedPaths: []string{"/other/link"}}).acquired(t)()
	writer.cancel()
	writer.canceled(t)
	reader.acquired(t)()
	release()
	assertIdle(t, &c)
}
