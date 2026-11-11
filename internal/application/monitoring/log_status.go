package monitoring

// ResultLogStatus is an optional port for a background writer's storage failures. Err
// reports the failure the writer is in now, and nil once it writes again, so a frontend can
// tell a failing log from one that recovered. It must be safe concurrently with writes and
// must not perform filesystem I/O.
type ResultLogStatus interface{ Err() error }
