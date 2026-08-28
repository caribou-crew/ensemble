package reckey

import (
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

func newTestRun(t *testing.T) runs.Paths {
	t.Helper()
	root := t.TempDir()
	p, err := runs.Create(root, "web", "checkout", "20260821T100000Z-aaaaaaa")
	if err != nil {
		t.Fatalf("runs.Create: %v", err)
	}
	return p
}

func TestResolveDataKeyOnAnUnencryptedRunIsNilNil(t *testing.T) {
	p := newTestRun(t)
	got, err := ResolveDataKey(p, t.TempDir())
	if err != nil {
		t.Fatalf("ResolveDataKey: %v", err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil for a run with no encryption.json", got)
	}
}

func TestResolveDataKeyDecryptsWithTheRightTeamKey(t *testing.T) {
	p := newTestRun(t)
	teamKey := bytes32('a')
	dataKey, err := GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := WrapDataKey(dataKey, teamKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.WriteEncryption(p, runs.Encryption{KeyID: KeyID(teamKey), WrappedDataKey: wrapped}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvTeamKey, string(hexEncode(teamKey)))

	got, err := ResolveDataKey(p, t.TempDir())
	if err != nil {
		t.Fatalf("ResolveDataKey: %v", err)
	}
	if string(got) != string(dataKey) {
		t.Fatal("resolved data key does not match the one that was wrapped")
	}
}

func TestResolveDataKeyWithNoTeamKeyIsNilNilNotAnError(t *testing.T) {
	p := newTestRun(t)
	teamKey := bytes32('b')
	dataKey, _ := GenerateDataKey()
	wrapped, err := WrapDataKey(dataKey, teamKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.WriteEncryption(p, runs.Encryption{KeyID: KeyID(teamKey), WrappedDataKey: wrapped}); err != nil {
		t.Fatal(err)
	}
	// No RETRACE_RECORDING_KEY, no keyfile.
	got, err := ResolveDataKey(p, t.TempDir())
	if err != nil {
		t.Fatalf("ResolveDataKey should not error when no team key resolves, got: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil data key with no team key present")
	}
}

func TestResolveDataKeyWithWrongTeamKeyIsNilNil(t *testing.T) {
	p := newTestRun(t)
	dataKey, _ := GenerateDataKey()
	wrapped, err := WrapDataKey(dataKey, bytes32('c'))
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.WriteEncryption(p, runs.Encryption{KeyID: KeyID(bytes32('c')), WrappedDataKey: wrapped}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvTeamKey, string(hexEncode(bytes32('d'))))

	got, err := ResolveDataKey(p, t.TempDir())
	if err != nil {
		t.Fatalf("ResolveDataKey should not error on a wrong key, got: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil data key when the wrong team key is present")
	}
}

func hexEncode(b []byte) []byte {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0xf]
	}
	return out
}
