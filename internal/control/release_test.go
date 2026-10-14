package control

import (
	"testing"
	"time"

	"appguard/internal/update"
)

func testRelease(max int) *agentRelease {
	return &agentRelease{
		manifest:      update.Manifest{Version: "2.0.0", SHA256: "aaa"},
		maxConcurrent: max,
		leases:        map[string]time.Time{},
		cooldown:      map[string]time.Time{},
	}
}

func TestOfferRespectsConcurrencyAndCapability(t *testing.T) {
	r := testRelease(1)
	hb := func(id, sha string, self bool) Heartbeat {
		return Heartbeat{AgentID: id, BinarySHA256: sha, SelfUpdate: self}
	}

	if r.offer(hb("old", "", false)) != nil {
		t.Fatal("agent without self-update must not be offered")
	}
	if r.offer(hb("a", "bbb", true)) == nil {
		t.Fatal("first agent should get the update")
	}
	if r.offer(hb("b", "bbb", true)) != nil {
		t.Fatal("second agent exceeds maxConcurrent")
	}
	if r.offer(hb("a", "bbb", true)) == nil {
		t.Fatal("lease holder keeps its offer")
	}
	if r.offer(hb("a", "AAA", true)) != nil {
		t.Fatal("updated agent needs nothing")
	}
	if r.offer(hb("b", "bbb", true)) == nil {
		t.Fatal("slot should be free after agent a updated")
	}
}

func TestOfferCooldownAfterExpiredLease(t *testing.T) {
	r := testRelease(1)
	h := Heartbeat{AgentID: "a", BinarySHA256: "bbb", SelfUpdate: true}
	r.offer(h)
	r.leases["a"] = time.Now().Add(-time.Second)

	if r.offer(h) != nil {
		t.Fatal("agent whose lease expired must wait for the cooldown")
	}
	if r.offer(Heartbeat{AgentID: "b", BinarySHA256: "bbb", SelfUpdate: true}) == nil {
		t.Fatal("expired lease must free the slot")
	}
}

func TestReleaseState(t *testing.T) {
	r := testRelease(1)
	cases := []struct {
		status map[string]any
		want   string
	}{
		{map[string]any{"binarySha256": "aaa", "selfUpdate": true, "version": "2.0.0"}, "current"},
		{map[string]any{"binarySha256": "bbb", "selfUpdate": true, "version": "1.9.0"}, "pending"},
		{map[string]any{"version": "1.1.4"}, "manual"},
		{map[string]any{"role": "controller", "version": "1.1.4"}, ""},
	}
	for _, c := range cases {
		if got := r.state(c.status); got != c.want {
			t.Errorf("state(%v) = %q, want %q", c.status, got, c.want)
		}
	}
	var disabled *agentRelease
	if disabled.state(cases[1].status) != "" || disabled.offer(Heartbeat{AgentID: "x", BinarySHA256: "b", SelfUpdate: true}) != nil {
		t.Fatal("disabled release must do nothing")
	}
}
