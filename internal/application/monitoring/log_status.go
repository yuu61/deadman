package monitoring

// ResultLogStatus is an optional port for a background writer's storage failures.
// Err must be safe concurrently with writes and must not perform filesystem I/O.
type ResultLogStatus interface{ Err() error }
