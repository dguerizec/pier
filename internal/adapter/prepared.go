package adapter

// Prepared is the non-disruptive result of validating and building a workload.
// AdapterData is an adapter-owned, secret-free teardown description which lets
// pier stop the applied workload even when the source manifest later changes.
type Prepared struct {
	AdapterData  []byte
	OverrideData []byte
}
