package service

import (
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

// replicaCacheView builds an AgentService with its OWN empty token cache,
// simulating a second master process (replica) over the same database.
func replicaCacheView(db *gorm.DB) *AgentService {
	return &AgentService{db: db, cache: &agentTokenCache{tokens: map[uint]string{}}}
}

// REV-AGENT-REPLICA-CACHE: a replica whose token cache is cold (restart, or
// never warmed by a heartbeat routed to the other replica) must still be
// able to serve RPC credentials — "online" node state is shared, but the
// plaintext token was process-local. Verifying the cached token against the
// shared token_hash row makes the cache self-healing: a stale entry (the
// peer rotated the token) is detected and rejected instead of being sent to
// the node as an old credential.
func TestReplicaRotationInvalidatesPeerCache(t *testing.T) {
	db := newIPPoolTestDB(t)
	// Replica A: creates the agent (token T1 cached + hashed in DB).
	a := NewAgentService(db)
	agent, t1, err := a.Create("replica-node", "")
	if err != nil {
		t.Fatal(err)
	}
	// Replica B: separate cache view over the same DB; it learned T1 (e.g.
	// from a heartbeat it served earlier).
	b := replicaCacheView(db)
	b.SeedRPCToken(agent.ID, t1)
	if got, err := b.RPCToken(agent.ID); err != nil || got != t1 {
		t.Fatalf("replica B baseline: %v %q", err, got)
	}

	// Replica A rotates: DB hash now covers T2; A's cache has T2.
	_, t2, err := a.RotateToken(agent.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Replica B's cached T1 is STALE. RPCToken must detect the mismatch
	// against the shared token_hash and refuse, never send the old
	// credential to the node.
	got, err := b.RPCToken(agent.ID)
	if err == nil {
		t.Fatalf("peer replica served rotated-away token: %q (want error)", got)
	}

	// After the agent heartbeats with T2 (reaching replica B this time),
	// B's cache repairs and serves T2.
	if _, err := b.Authenticate(t2); err != nil {
		t.Fatalf("authenticate with rotated token: %v", err)
	}
	got, err = b.RPCToken(agent.ID)
	if err != nil || got != t2 {
		t.Fatalf("replica B did not repair after authenticated heartbeat: %v %q", err, got)
	}
}

// REV-AGENT-REPLICA-CACHE: a token cached from a row that has since been
// DELETED must not be served either.
func TestReplicaDeleteInvalidatesPeerCache(t *testing.T) {
	db := newIPPoolTestDB(t)
	a := NewAgentService(db)
	agent, t1, err := a.Create("replica-node-2", "")
	if err != nil {
		t.Fatal(err)
	}
	b := replicaCacheView(db)
	b.SeedRPCToken(agent.ID, t1)

	if err := a.Delete(agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RPCToken(agent.ID); err == nil {
		t.Fatal("peer replica served a deleted agent's token")
	}
}

var _ = model.AgentStatusOnline
