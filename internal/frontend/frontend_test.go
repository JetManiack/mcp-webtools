package frontend

import (
	"io"
	"strings"
	"testing"
)

// The embedded tree must contain index.html at its root, not under a
// static/ prefix — FS strips that, and getting it wrong serves 404 at / with
// a perfectly green build.
func TestFSServesIndexAtRoot(t *testing.T) {
	fsys, err := FS(false)
	if err != nil {
		t.Fatalf("FS: %v", err)
	}

	file, err := fsys.Open("/index.html")
	if err != nil {
		t.Fatalf("open /index.html: %v", err)
	}
	defer file.Close()

	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(body), `id="root"`) {
		t.Error(`index.html is missing the id="root" mount point the SPA renders into`)
	}
	// The bundle is produced by `make generate` (esbuild bundles React directly
	// into it), so its absence from a bare checkout is expected — but index.html
	// must still be the thing that asks for it.
	if !strings.Contains(string(body), "/js/app.bundle.js") {
		t.Error("index.html does not load /js/app.bundle.js")
	}
}

func TestFSDevelReadsFromDisk(t *testing.T) {
	fsys, err := FS(true)
	if err != nil {
		t.Fatalf("FS(devel): %v", err)
	}
	if fsys == nil {
		t.Fatal("FS(devel) returned nil")
	}
}
