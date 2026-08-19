# MeowAgent memory module context (data layer, standalone contract)

> Package lives at internal/memory — sealed from external import; hosts use only the api package API (Memory/Record/Query aliases).

## Purpose

- Standalone data-layer contract: memory CRUD interface and pure data structs for host reference
- The framework does NOT consume this package — memory is host-managed via Organs.Context (MemHop pattern)

## Dependencies

- Go standard library only (context)

## Interface Contract

- `Memory` interface (host-implemented reference contract):
  - `Save(ctx, Record)`: saves a record (Key is unique)
  - `Recall(ctx, Query)`: retrieves by CellID/Prefix/Kind/Limit (Limit<=0 = unlimited)
  - `Forget(ctx, key, cellID)`: deletes by Key+CellID
- `Record{Key, CellID, Kind, Content, Created}`: pure struct, no methods
- `Query{CellID, Prefix, Kind, Limit}`: pure struct, no methods

## Key Decisions

- No stubs, no sentinel errors: the package holds contract types only (no "minimal runnable" path)
- No out-of-box implementation provided (no MemVault or similar default backend)
- Hosts integrate memory into the loop by maintaining Organs.Context between Stimulate calls

## Pitfalls

- Record/Query must not have methods (pure data contracts)
- Do not import upper packages (dependency direction is strictly downward; this package is standalone)
- Do not re-add Unimplemented* stubs — hosts implement the contract themselves
