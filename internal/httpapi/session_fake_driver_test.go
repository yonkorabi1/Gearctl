package httpapi

import (
	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/transport"
)

// fakeSessionDriver is a drivers.CliDriver double implementing every
// optional capability (FactsProvider/InterfaceProvider/
// LLDPNeighborsProvider/ConfigGetter/ConfigLoader), used ONLY to test
// each handler's session-based code path. Registered directly into
// sessionStore via sessionStore.Open() -- bypassing Connect()/a real
// network connection entirely, since sessionpool.Store.Open() never
// dials anything itself (see its doc comment). This is what lets these
// tests exercise a genuine SUCCESS response for the session path, unlike
// every fresh-connection handler test in this package, which can only
// exercise validation/error paths without a real reachable device (see
// e.g. TestExecuteHandlerReturnsFailedForConnectionError).
type fakeSessionDriver struct {
	vendor string

	facts    map[string]interface{}
	factsErr error

	interfaces    map[string]drivers.InterfaceInfo
	interfacesErr error

	neighbors    map[string][]drivers.LLDPNeighbor
	neighborsErr error

	config    string
	configErr error

	runResults []transport.CommandResult
	runErr     error

	diff, configHash string
	reverseCommands  []string
	applied          bool
	loadErr          error

	disconnectCount int
}

func (f *fakeSessionDriver) Vendor() string                           { return f.vendor }
func (f *fakeSessionDriver) Connect(transport.ConnectionParams) error { return nil }
func (f *fakeSessionDriver) RunCommands(_ []string) ([]transport.CommandResult, error) {
	return f.runResults, f.runErr
}
func (f *fakeSessionDriver) Disconnect() error {
	f.disconnectCount++
	return nil
}
func (f *fakeSessionDriver) GetFacts() (map[string]interface{}, error) {
	return f.facts, f.factsErr
}
func (f *fakeSessionDriver) GetInterfaces() (map[string]drivers.InterfaceInfo, error) {
	return f.interfaces, f.interfacesErr
}
func (f *fakeSessionDriver) GetLLDPNeighbors() (map[string][]drivers.LLDPNeighbor, error) {
	return f.neighbors, f.neighborsErr
}
func (f *fakeSessionDriver) GetConfig() (string, error) {
	return f.config, f.configErr
}
func (f *fakeSessionDriver) LoadConfig(_ string, _ bool, _ string) (string, string, bool, error) {
	return f.diff, f.configHash, f.applied, f.loadErr
}
func (f *fakeSessionDriver) LoadMergeCommands(_ []string, _ bool, _ string) (string, []string, string, bool, error) {
	return f.diff, f.reverseCommands, f.configHash, f.applied, f.loadErr
}

var (
	_ drivers.CliDriver             = (*fakeSessionDriver)(nil)
	_ drivers.FactsProvider         = (*fakeSessionDriver)(nil)
	_ drivers.InterfaceProvider     = (*fakeSessionDriver)(nil)
	_ drivers.LLDPNeighborsProvider = (*fakeSessionDriver)(nil)
	_ drivers.ConfigGetter          = (*fakeSessionDriver)(nil)
	_ drivers.ConfigLoader          = (*fakeSessionDriver)(nil)
)

// openFakeSession registers driver in the shared sessionStore and
// returns its session ID, for tests to include as the "sessionId" field
// in a request body.
func openFakeSession(driver drivers.CliDriver, vendor string) string {
	sess, err := sessionStore.Open(driver, vendor)
	if err != nil {
		panic("openFakeSession: " + err.Error()) // test setup failure, not a case under test
	}
	return sess.ID
}
