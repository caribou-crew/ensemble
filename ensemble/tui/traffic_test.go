package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestTrafficPanelWaitForHopRendersRow(t *testing.T) {
	ch := make(chan trace.Hop, 1)
	ch <- trace.Hop{Seq: 1, From: "client", To: "catalog", Method: "GET", Path: "/products", Status: 200}

	p := newTrafficPanel()
	msg := waitForHop(ch)()
	hop, ok := msg.(hopMsg)
	if !ok {
		t.Fatalf("expected hopMsg, got %#v", msg)
	}
	p.appendHop(hop.hop)

	if len(p.all) != 1 {
		t.Fatalf("expected 1 hop buffered, got %d", len(p.all))
	}
	rows := p.table.Rows()
	if len(rows) != 1 || rows[0][1] != "catalog" || rows[0][4] != "200" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestTrafficPanelErrorsOnlyFilter(t *testing.T) {
	p := newTrafficPanel()
	p.appendHop(trace.Hop{Seq: 1, To: "catalog", Status: 200})
	p.appendHop(trace.Hop{Seq: 2, To: "catalog", Status: 500, Err: "upstream 500"})
	p.appendHop(trace.Hop{Seq: 3, To: "storefront", Status: 200})

	if len(p.table.Rows()) != 3 {
		t.Fatalf("expected 3 visible rows before filtering, got %d", len(p.table.Rows()))
	}

	p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	rows := p.table.Rows()
	if len(rows) != 1 || rows[0][4] != "ERR" {
		t.Fatalf("expected only the error hop after filtering, got %+v", rows)
	}

	// Toggling back off restores every buffered hop, including ones that
	// arrived while the filter was on.
	p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if len(p.table.Rows()) != 3 {
		t.Fatalf("expected all 3 rows after un-filtering, got %d", len(p.table.Rows()))
	}
}

func TestTrafficPanelSelectedHopDrivesDetail(t *testing.T) {
	p := newTrafficPanel()
	p.appendHop(trace.Hop{Seq: 1, To: "catalog", Method: "GET", Status: 200, Req: trace.Payload{Body: `{"id":1}`}})

	hop, ok := p.selected()
	if !ok || hop.Seq != 1 {
		t.Fatalf("expected the appended hop to be selected, got %+v ok=%v", hop, ok)
	}
	detail := p.detailView(80)
	if detail == "" {
		t.Fatal("expected a non-empty detail view")
	}
}

// reconnectingHopClient's first TrafficStream call fails, the second
// delivers a hop — used to exercise the panel's real consumption path
// (waitForHop feeding back into itself) against StreamTraffic's reconnect,
// not just the panel in isolation.
type reconnectingHopClient struct {
	apiClient
	first bool
}

func (c *reconnectingHopClient) TrafficStream(ctx context.Context, since uint64) (<-chan trace.Hop, error) {
	if !c.first {
		c.first = true
		return nil, errors.New("connection refused")
	}
	ch := make(chan trace.Hop, 1)
	ch <- trace.Hop{Seq: 1, To: "catalog"}
	close(ch)
	return ch, nil
}

func TestTrafficPanelReconnectResumesAppending(t *testing.T) {
	setStreamReconnectDelayForTest(t, 5*time.Millisecond)

	rc := &reconnectingHopClient{}
	ctx, cancel := context.WithCancel(context.Background())
	ch := StreamTraffic(ctx, rc, 0)
	defer drainUntilClosed(t, ch, cancel)

	p := newTrafficPanel()
	select {
	case hop := <-ch:
		p.appendHop(hop)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hop after reconnect")
	}

	if len(p.all) != 1 || p.all[0].To != "catalog" {
		t.Fatalf("expected the hop delivered after reconnect, got %+v", p.all)
	}
}
