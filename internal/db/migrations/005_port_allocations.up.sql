-- Migration: 005_port_allocations
-- Description: Port allocation tracking for OpenCode server instances
-- Created: 2025-12-27

-- ============================================================================
-- PORT_ALLOCATIONS TABLE
-- ============================================================================
-- Tracks port allocations for OpenCode servers to prevent conflicts.
-- Each agent running OpenCode gets a dedicated port from a pool (17000-17999).
--
-- Design: We delete released allocations rather than soft-delete. This allows
-- ports to be reused immediately without unique constraint complications.

CREATE TABLE IF NOT EXISTS port_allocations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    
    -- The allocated port number
    port INTEGER NOT NULL,
    
    -- The node this port is allocated on (ports are node-local)
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    
    -- The agent using this port (nullable - port can be reserved but unassigned)
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    
    -- Human-readable reason for allocation
    reason TEXT,
    
    -- When the allocation was created
    allocated_at TEXT NOT NULL DEFAULT (datetime('now')),
    
    -- Unique constraint: only one allocation per port per node at a time
    UNIQUE(node_id, port)
);

-- Index for finding ports by agent
CREATE INDEX IF NOT EXISTS idx_port_allocations_agent 
    ON port_allocations(agent_id);

-- Index for finding allocations on a node
CREATE INDEX IF NOT EXISTS idx_port_allocations_node 
    ON port_allocations(node_id);
