// peer_service/internal/p2p/rbac_test.go
package p2p

import (
	"testing"

	"peer_service/internal/roles"
	p2ppb "peer_service/proto/p2p"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Mock RoleManager behavior since we only need GetPermissionsForPeer
// But roles.RoleManager is a struct, checking if we can mock it easily.
// It seems we passed *roles.RoleManager.
// We can instantiate a real RoleManager and configure it.

func TestQueueController_ApplyUpdate_RBAC(t *testing.T) {
	// 1. Setup
	// Create a RoleManager
	rm := roles.NewRoleManager()

	// Create QueueController
	qc := NewQueueController(func() {}) // triggerDiscontinuity no-op

	// Define Peers
	// Use valid crypto keys to generate valid peer IDs so Decode works
	priv1, _, _ := crypto.GenerateEd25519Key(nil)
	adminID, _ := peer.IDFromPrivateKey(priv1)

	priv2, _, _ := crypto.GenerateEd25519Key(nil)
	maliciousID, _ := peer.IDFromPrivateKey(priv2)

	// Assign roles
	// Admin has PermQueue (via 'Admin' role implicitly or default 'Viewer' lacking it?)
	// Default 'Viewer' usually has limited perms.
	// Let's create an 'Admin' role with PermQueue and 'Viewer' without.

	// Note: roles package defaults might need setup.
	// Let's explicitly set assignments in RM.
	rm.UpdateDefinition("Admin", roles.PermQueue|roles.PermStream)
	rm.UpdateDefinition("Viewer", roles.PermPlayPause)

	rm.SetPeerRoles(adminID, []string{"Admin"})
	rm.SetPeerRoles(maliciousID, []string{"Viewer"})

	// 2. Scenario: Admin sends Update
	// Should Succeed
	updateMsg := &p2ppb.QueueUpdate{
		Items: []*p2ppb.QueueItem{
			{
				FilePath: "admin_video.mp4",
				StoredBy: adminID.String(),
				AddedBy:  adminID.String(),
			},
		},
		HlcTs: 100,
	}

	err := qc.ApplyUpdate(updateMsg, adminID, nil, nil, rm)
	if err != nil {
		t.Errorf("Expected Admin update to succeed, got error: %v", err)
	}

	// Verify queue has item
	items := qc.Q().Items()
	if len(items) != 1 || items[0].FilePath != "admin_video.mp4" {
		t.Errorf("Queue should contain admin video")
	}

	// 3. Scenario: Malicious Peer sends Update
	// Should Fail
	maliciousUpdate := &p2ppb.QueueUpdate{
		Items: []*p2ppb.QueueItem{
			{
				FilePath: "hacked_video.mp4",
				StoredBy: maliciousID.String(),
				AddedBy:  maliciousID.String(),
			},
		},
		HlcTs: 101,
	}

	err = qc.ApplyUpdate(maliciousUpdate, maliciousID, nil, nil, rm)
	if err == nil {
		t.Error("Expected Malicious update to fail, but it succeeded")
	} else {
		t.Logf("Correctly rejected malicious update: %v", err)
	}

	// Verify queue still has old state (admin video)
	items = qc.Q().Items()
	if len(items) != 1 || items[0].FilePath != "admin_video.mp4" {
		t.Errorf("Queue should still contain admin video, found: %v", items)
	}
}
