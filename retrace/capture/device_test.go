package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// deviceSession builds the minimum Session the Device method reads: a run
// directory. Nothing else on a Session is involved, which is the point —
// resolving the screen must not require a live proxy or a started run.
func deviceSession(t *testing.T) *Session {
	t.Helper()
	return &Session{Paths: runs.Paths{RunDir: t.TempDir()}}
}

func writeDeviceFile(t *testing.T, s *Session, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Paths.RunDir, DeviceFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAnAdaptersDeviceFileIsUsedVerbatim(t *testing.T) {
	s := deviceSession(t)
	writeDeviceFile(t, s, `{"kind":"browser","id":"chromium","width":390,"height":844,"scale":2}`)

	// Shots are passed too, and must NOT win: an adapter that reported its
	// viewport knows more than a screenshot's dimensions do.
	got, err := s.Device([]runs.Checkpoint{{Name: "cart", Width: 1170, Height: 2532}})
	if err != nil {
		t.Fatal(err)
	}
	want := runs.Device{Kind: "browser", ID: "chromium", Width: 390, Height: 844, Scale: 2}
	if got == nil || *got != want {
		t.Errorf("Device = %+v, want %+v", got, want)
	}
}

func TestWithNoDeviceFileTheFirstShotIsTheEvidence(t *testing.T) {
	// Without this fallback the guard is nil for every run captured by an
	// adapter that writes no device.json — including every run captured
	// before this existed — and a guard that switches itself off for most
	// runs is not a guard.
	s := deviceSession(t)
	got, err := s.Device([]runs.Checkpoint{
		{Name: "cart", Width: 390, Height: 844},
		{Name: "login", Width: 999, Height: 999},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := runs.Device{Kind: "shot", ID: "cart", Width: 390, Height: 844}
	if got == nil || *got != want {
		t.Errorf("Device = %+v, want %+v — the FIRST checkpoint (Checkpoints sorts by name, so this is deterministic)", got, want)
	}
	if got.Kind != "shot" {
		t.Error(`the fallback must label itself "shot" — a reader comparing two of these needs to know they are screenshots, not viewports`)
	}
}

func TestNoDeviceFileAndNoShotsIsNoEvidence(t *testing.T) {
	// Stated as absence rather than invented. A wire-only run has no screen,
	// and a 0x0 stand-in would compare equal to the next broken run.
	s := deviceSession(t)
	got, err := s.Device(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("Device = %+v, want nil", got)
	}
}

func TestADeviceFileThatIsNotAScreenIsAnError(t *testing.T) {
	// Refused at the door: downstream, 0x0 is indistinguishable from a fact,
	// and two runs that both wrote one would agree they matched.
	for _, body := range []string{
		`{"kind":"browser","width":0,"height":844}`,
		`{"kind":"browser","width":390,"height":0}`,
		`{"kind":"browser","width":-5,"height":844}`,
		`{}`,
	} {
		s := deviceSession(t)
		writeDeviceFile(t, s, body)
		if _, err := s.Device(nil); err == nil {
			t.Errorf("Device accepted %s", body)
		}
	}
}

func TestAnUnreadableDeviceFileIsAnErrorNotASilentFallback(t *testing.T) {
	// Falling back to the shot here would mean an adapter with a bug in its
	// JSON writer produces runs that quietly disagree with what it believes
	// it recorded — and the comparison would then pass or fail on geometry
	// nobody chose.
	s := deviceSession(t)
	writeDeviceFile(t, s, `{"kind":"browser","width":`)
	_, err := s.Device([]runs.Checkpoint{{Name: "cart", Width: 390, Height: 844}})
	if err == nil {
		t.Fatal("a truncated device.json fell through to the shot fallback")
	}
	if !strings.Contains(err.Error(), DeviceFile) {
		t.Errorf("the error does not name the file: %v", err)
	}
}

func TestADeviceFileWithNoKindStillCountsAsOneTheAdapterWrote(t *testing.T) {
	// The adapter told us something real. Defaulting the provenance beats
	// refusing the run — but it must not be defaulted to "browser", which
	// would put an assertion on the record nobody made.
	s := deviceSession(t)
	writeDeviceFile(t, s, `{"width":390,"height":844}`)
	got, err := s.Device(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Kind != "device" {
		t.Errorf("Device = %+v, want kind \"device\"", got)
	}
}
