package mygo

import "testing"

func TestPosseSameDocumentHistory(t *testing.T) {
	native := &webViewNative{}
	window := &Window{native: native}
	handler := &windowHandler{window}

	window.noteWebViewNavigation(webNavigationHistory)
	handler.NavigationStayedInDocument()
	if native.navigationPhase != webNavigationIdle || native.navigationRevision != 2 {
		t.Fatalf("same-document history left phase %d, revision %d", native.navigationPhase, native.navigationRevision)
	}

	window.noteWebViewNavigation(webNavigationHistory)
	handler.NavigationStarted("https://example.com/new")
	handler.NavigationStayedInDocument()
	if native.navigationPhase != webNavigationPending || native.navigationRevision != 4 {
		t.Fatalf("same-document event completed a new document navigation: phase %d, revision %d", native.navigationPhase, native.navigationRevision)
	}
}
