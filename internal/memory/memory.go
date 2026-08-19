// memory.go — data layer: memory CRUD contract (bottom-most package).
//
// This package depends on nothing above it: it holds only pure interfaces
// and data structs for host reference. The framework itself does not
// consume this contract — hosts inject memory via Organs.Context (MemHop).

package memory

import "context"

// Record is a single memory record.
// Key is the unique key, CellID the owning agent, Kind the category,
// Content the payload, Created the Unix-second timestamp.
type Record struct {
	Key     string
	CellID  string
	Kind    string
	Content []byte
	Created int64
}

// Query is a memory recall condition (pure data, no methods).
// Empty CellID matches all owners; empty Prefix/Kind skip those filters;
// Limit <= 0 means unlimited.
type Query struct {
	CellID string
	Prefix string
	Kind   string
	Limit  int
}

// Memory is the memory backend host port (data layer): a reference
// contract for save/recall/forget. Implementations are host-injected;
// the framework provides no default backend.
type Memory interface {
	Save(ctx context.Context, r Record) error
	Recall(ctx context.Context, q Query) ([]Record, error)
	Forget(ctx context.Context, key, cellID string) error
}
