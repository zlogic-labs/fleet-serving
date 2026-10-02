package report

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/zlogic-labs/fleet/core/pkg/inventory"
)

const goldenPath = "testdata/report.v1.json"

func golden(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return b
}

func goldenReport(t *testing.T) inventory.Report {
	t.Helper()
	var rep inventory.Report
	if err := json.Unmarshal(golden(t), &rep); err != nil {
		t.Fatalf("golden does not decode: %v", err)
	}
	return rep
}

// What the control plane actually receives. Testing the bytes on the wire
// rather than the struct is the whole point: a field the reporter forgets to
// fill is invisible until a console cell renders empty.
func TestWhatWePutOnTheWireIsTheContract(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("reported with %s, contract says PUT", r.Method)
		}
		if r.URL.Path != inventory.Path {
			t.Errorf("reported to %s, contract says %s", r.URL.Path, inventory.Path)
		}
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	want := goldenReport(t)
	// Zeroed on purpose. The golden carries a version because it is what the
	// control plane receives, so leaving it set would make this test pass even
	// if the reporter stopped stamping it — the value would arrive from the
	// input instead. Only the reporter is allowed to fill it in.
	want.Contract = 0
	if err := New(srv.URL, want.Cluster.Name).Send(context.Background(), want); err != nil {
		t.Fatalf("send: %v", err)
	}

	var sent inventory.Report
	if err := json.Unmarshal(got, &sent); err != nil {
		t.Fatalf("what we sent does not decode: %v", err)
	}
	if sent.Contract != inventory.ContractVersion {
		t.Errorf("sent contract %d, want %d", sent.Contract, inventory.ContractVersion)
	}
	reencoded, err := json.MarshalIndent(sent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if w := bytes.TrimRight(golden(t), "\n"); !bytes.Equal(reencoded, w) {
		t.Errorf("the report we send is not the contract\n--- got ---\n%s\n--- want ---\n%s", reencoded, w)
	}
}

// From the consumer's side: the shared type still means what the golden says
// it means. Core's own test catches a wire change; this one catches it in the
// repository that has to live with it.
func TestTheContractGoldenRoundTrips(t *testing.T) {
	sent, err := json.MarshalIndent(goldenReport(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if want := bytes.TrimRight(golden(t), "\n"); !bytes.Equal(sent, want) {
		t.Errorf("the golden does not round-trip\n--- got ---\n%s\n--- want ---\n%s", sent, want)
	}
}

// An unreachable control plane must be visible rather than dropped: the caller
// is a loop, and a loop that cannot tell a failed report from a successful one
// will happily report nothing forever.
func TestAFailedReportIsReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := New(srv.URL, "c").Send(context.Background(), inventory.Report{}); err == nil {
		t.Error("a rejected report returned no error")
	}
	if err := New("", "c").Send(context.Background(), inventory.Report{}); err == nil {
		t.Error("reporting with no control plane returned no error")
	}
}
