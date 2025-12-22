package node

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
)

func setupTestService(t *testing.T) (*Service, func()) {
	t.Helper()

	testDB, err := db.OpenInMemory()
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}

	if err := testDB.Migrate(context.Background()); err != nil {
		testDB.Close()
		t.Fatalf("failed to migrate database: %v", err)
	}

	repo := db.NewNodeRepository(testDB)
	service := NewService(repo, WithDefaultTimeout(5*time.Second))

	cleanup := func() {
		testDB.Close()
	}

	return service, cleanup
}

func TestNewService(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	if service == nil {
		t.Fatal("expected non-nil service")
	}

	if service.DefaultTimeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", service.DefaultTimeout)
	}
}

func TestAddNode(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	tests := []struct {
		name    string
		node    *models.Node
		wantErr bool
	}{
		{
			name: "local node",
			node: &models.Node{
				Name:    "local",
				IsLocal: true,
			},
			wantErr: false,
		},
		{
			name: "remote node with valid SSH target",
			node: &models.Node{
				Name:      "remote1",
				SSHTarget: "user@host.example.com:22",
			},
			wantErr: false,
		},
		{
			name: "remote node without port",
			node: &models.Node{
				Name:      "remote2",
				SSHTarget: "user@host.example.com",
			},
			wantErr: false,
		},
		{
			name: "remote node host only",
			node: &models.Node{
				Name:      "remote3",
				SSHTarget: "host.example.com",
			},
			wantErr: false,
		},
		{
			name: "invalid empty name",
			node: &models.Node{
				Name:      "",
				SSHTarget: "user@host:22",
			},
			wantErr: true,
		},
		{
			name: "remote node missing SSH target",
			node: &models.Node{
				Name:    "remote-no-target",
				IsLocal: false,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.AddNode(ctx, tt.node, false)

			if tt.wantErr && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if !tt.wantErr {
				// Verify node was created
				got, err := service.GetNode(ctx, tt.node.ID)
				if err != nil {
					t.Errorf("failed to get created node: %v", err)
				}
				if got.Name != tt.node.Name {
					t.Errorf("name mismatch: got %s, want %s", got.Name, tt.node.Name)
				}
			}
		})
	}
}

func TestAddNodeDuplicate(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	node := &models.Node{
		Name:    "test-node",
		IsLocal: true,
	}

	// First add should succeed
	if err := service.AddNode(ctx, node, false); err != nil {
		t.Fatalf("first add failed: %v", err)
	}

	// Second add with same name should fail
	node2 := &models.Node{
		Name:    "test-node",
		IsLocal: true,
	}
	err := service.AddNode(ctx, node2, false)
	if err == nil {
		t.Error("expected error for duplicate node")
	}
	if err != ErrNodeAlreadyExists {
		t.Errorf("expected ErrNodeAlreadyExists, got %v", err)
	}
}

func TestRemoveNode(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Add a node
	node := &models.Node{
		Name:    "to-remove",
		IsLocal: true,
	}
	if err := service.AddNode(ctx, node, false); err != nil {
		t.Fatalf("failed to add node: %v", err)
	}

	// Remove it
	if err := service.RemoveNode(ctx, node.ID); err != nil {
		t.Errorf("failed to remove node: %v", err)
	}

	// Verify it's gone
	_, err := service.GetNode(ctx, node.ID)
	if err != ErrNodeNotFound {
		t.Errorf("expected ErrNodeNotFound, got %v", err)
	}
}

func TestRemoveNodeNotFound(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	err := service.RemoveNode(ctx, "nonexistent-id")
	if err != ErrNodeNotFound {
		t.Errorf("expected ErrNodeNotFound, got %v", err)
	}
}

func TestListNodes(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Add some nodes
	nodes := []*models.Node{
		{Name: "node1", IsLocal: true, Status: models.NodeStatusOnline},
		{Name: "node2", SSHTarget: "user@host1:22", Status: models.NodeStatusOnline},
		{Name: "node3", SSHTarget: "user@host2:22", Status: models.NodeStatusOffline},
	}

	for _, n := range nodes {
		if err := service.AddNode(ctx, n, false); err != nil {
			t.Fatalf("failed to add node %s: %v", n.Name, err)
		}
	}

	// List all nodes
	all, err := service.ListNodes(ctx, nil)
	if err != nil {
		t.Fatalf("failed to list nodes: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(all))
	}

	// List online nodes only
	online := models.NodeStatusOnline
	onlineNodes, err := service.ListNodes(ctx, &online)
	if err != nil {
		t.Fatalf("failed to list online nodes: %v", err)
	}
	if len(onlineNodes) != 2 {
		t.Errorf("expected 2 online nodes, got %d", len(onlineNodes))
	}

	// List offline nodes only
	offline := models.NodeStatusOffline
	offlineNodes, err := service.ListNodes(ctx, &offline)
	if err != nil {
		t.Fatalf("failed to list offline nodes: %v", err)
	}
	if len(offlineNodes) != 1 {
		t.Errorf("expected 1 offline node, got %d", len(offlineNodes))
	}
}

func TestGetNode(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	node := &models.Node{
		Name:       "test-get",
		SSHTarget:  "user@example.com:22",
		SSHBackend: models.SSHBackendNative,
	}

	if err := service.AddNode(ctx, node, false); err != nil {
		t.Fatalf("failed to add node: %v", err)
	}

	got, err := service.GetNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	if got.Name != node.Name {
		t.Errorf("name mismatch: got %s, want %s", got.Name, node.Name)
	}
	if got.SSHTarget != node.SSHTarget {
		t.Errorf("SSH target mismatch: got %s, want %s", got.SSHTarget, node.SSHTarget)
	}
	if got.SSHBackend != node.SSHBackend {
		t.Errorf("SSH backend mismatch: got %s, want %s", got.SSHBackend, node.SSHBackend)
	}
}

func TestGetNodeByName(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	node := &models.Node{
		Name:    "named-node",
		IsLocal: true,
	}

	if err := service.AddNode(ctx, node, false); err != nil {
		t.Fatalf("failed to add node: %v", err)
	}

	got, err := service.GetNodeByName(ctx, "named-node")
	if err != nil {
		t.Fatalf("failed to get node by name: %v", err)
	}

	if got.ID != node.ID {
		t.Errorf("ID mismatch: got %s, want %s", got.ID, node.ID)
	}
}

func TestGetNodeNotFound(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	_, err := service.GetNode(ctx, "nonexistent")
	if err != ErrNodeNotFound {
		t.Errorf("expected ErrNodeNotFound, got %v", err)
	}

	_, err = service.GetNodeByName(ctx, "nonexistent")
	if err != ErrNodeNotFound {
		t.Errorf("expected ErrNodeNotFound, got %v", err)
	}
}

func TestUpdateNode(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	node := &models.Node{
		Name:    "to-update",
		IsLocal: true,
	}

	if err := service.AddNode(ctx, node, false); err != nil {
		t.Fatalf("failed to add node: %v", err)
	}

	// Update the node
	node.Name = "updated-name"
	if err := service.UpdateNode(ctx, node); err != nil {
		t.Errorf("failed to update node: %v", err)
	}

	// Verify update
	got, err := service.GetNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("failed to get updated node: %v", err)
	}
	if got.Name != "updated-name" {
		t.Errorf("name not updated: got %s, want updated-name", got.Name)
	}
}

func TestTestConnectionLocal(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	node := &models.Node{
		Name:    "local-test",
		IsLocal: true,
	}

	result, err := service.TestConnection(ctx, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Success {
		t.Error("expected local connection to succeed")
	}

	if result.Metadata.Platform != "local" {
		t.Errorf("expected platform 'local', got %s", result.Metadata.Platform)
	}
}

func TestParseSSHTarget(t *testing.T) {
	tests := []struct {
		target   string
		wantUser string
		wantHost string
		wantPort int
	}{
		{
			target:   "host.example.com",
			wantUser: "",
			wantHost: "host.example.com",
			wantPort: 22,
		},
		{
			target:   "user@host.example.com",
			wantUser: "user",
			wantHost: "host.example.com",
			wantPort: 22,
		},
		{
			target:   "host.example.com:2222",
			wantUser: "",
			wantHost: "host.example.com",
			wantPort: 2222,
		},
		{
			target:   "user@host.example.com:2222",
			wantUser: "user",
			wantHost: "host.example.com",
			wantPort: 2222,
		},
		{
			target:   "root@192.168.1.100:22",
			wantUser: "root",
			wantHost: "192.168.1.100",
			wantPort: 22,
		},
		{
			target:   "192.168.1.100",
			wantUser: "",
			wantHost: "192.168.1.100",
			wantPort: 22,
		},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			user, host, port := ParseSSHTarget(tt.target)

			if user != tt.wantUser {
				t.Errorf("user: got %s, want %s", user, tt.wantUser)
			}
			if host != tt.wantHost {
				t.Errorf("host: got %s, want %s", host, tt.wantHost)
			}
			if port != tt.wantPort {
				t.Errorf("port: got %d, want %d", port, tt.wantPort)
			}
		})
	}
}

func TestValidateSSHTarget(t *testing.T) {
	tests := []struct {
		target  string
		wantErr bool
	}{
		{"user@host:22", false},
		{"host:22", false},
		{"host", false},
		{"user@host", false},
		{"192.168.1.1:2222", false},
		{"", true},
		{"user@:22", true},        // empty host
		{"user@host:0", true},     // invalid port
		{"user@host:99999", true}, // port out of range
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			err := validateSSHTarget(tt.target)
			if tt.wantErr && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSSHBackendDefaults(t *testing.T) {
	service, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	node := &models.Node{
		Name:      "default-backend",
		SSHTarget: "user@host:22",
	}

	if err := service.AddNode(ctx, node, false); err != nil {
		t.Fatalf("failed to add node: %v", err)
	}

	got, err := service.GetNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	if got.SSHBackend != models.SSHBackendAuto {
		t.Errorf("expected default backend to be 'auto', got %s", got.SSHBackend)
	}

	if got.Status != models.NodeStatusUnknown {
		t.Errorf("expected default status to be 'unknown', got %s", got.Status)
	}
}
