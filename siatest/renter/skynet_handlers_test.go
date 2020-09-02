package renter

import (
	"net/http"
	"testing"

	"os"
	"path/filepath"
	"strings"

	"gitlab.com/NebulousLabs/Sia/node"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/siatest"
	"gitlab.com/NebulousLabs/Sia/siatest/dependencies"
	"gitlab.com/NebulousLabs/errors"
)

// TestSkynetSkylinkHandlerGET tests the behaviour of SkynetSkylinkHandlerGET
// when it handles different combinations of metadata and content. These tests
// use the fixtures in `testdata/skylink_fixtures.json`.
func TestSkynetSkylinkHandlerGET(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// Create a testgroup.
	groupParams := siatest.GroupParams{
		Hosts:  3,
		Miners: 1,
	}
	testDir := siatest.TestDir("renter", t.Name())
	if err := os.MkdirAll(testDir, persist.DefaultDiskPermissionsTest); err != nil {
		t.Fatal(err)
	}
	tg, err := siatest.NewGroupFromTemplate(testDir, groupParams)
	if err != nil {
		t.Fatal("Failed to create group: ", err)
	}
	defer func() {
		if err := tg.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Add a Renter node.
	renterParams := node.Renter(filepath.Join(testDir, "renter"))
	renterParams.RenterDeps = &dependencies.DependencyResolveSkylinkToFixture{}
	nodes, err := tg.AddNodes(renterParams)
	if err != nil {
		t.Fatal(err)
	}
	r := nodes[0]
	defer func() { _ = tg.RemoveNode(r) }()

	redirectErrStr := "Redirect"
	subTests := []struct {
		Name          string
		Skylink       string
		ExpectedError string
	}{
		{
			// ValidSkyfile is the happy path, ensuring that we don't get errors
			// on valid data.
			Name:          "ValidSkyfile",
			Skylink:       "_A6d-2CpM2OQ-7m5NPAYW830NdzC3wGydFzzd-KnHXhwJA",
			ExpectedError: "",
		},
		{
			// SingleFileDefaultPath ensures that we return an error if a single
			// file has a `defaultpath` field.
			Name:          "SingleFileDefaultPath",
			Skylink:       "3AAcCO73xMbehYaK7bjDGCtW0GwOL6Swl-lNY52Pb_APzA",
			ExpectedError: "defaultpath is not allowed on single files",
		},
		{
			// DefaultPathDisableDefaultPath ensures that we return an error if
			// a file has both defaultPath and disableDefaultPath set.
			Name:          "DefaultPathDisableDefaultPath",
			Skylink:       "3BBcCO73xMbehYaK7bjDGCtW0GwOL6Swl-lNY52Pb_APzA",
			ExpectedError: "both defaultpath and disabledefaultpath are set",
		},
		{
			// NonRootDefaultPath ensures that we return an error if a file has
			// a non-root defaultPath.
			Name:          "NonRootDefaultPath",
			Skylink:       "4BBcCO73xMbehYaK7bjDGCtW0GwOL6Swl-lNY52Pb_APzA",
			ExpectedError: "which refers to a non-root file",
		},
		{
			// DetectRedirect ensures that if the skylink doesn't have a
			// trailing slash and has a default path that results in an HTML
			// file we redirect to the same skylink with a trailing slash.
			Name:          "DetectRedirect",
			Skylink:       "4CCcCO73xMbehYaK7bjDGCtW0GwOL6Swl-lNY52Pb_APzA",
			ExpectedError: redirectErrStr,
		},
		{
			// EnsureNoRedirect ensures that there is no redirect if the skylink
			// has a trailing slash.
			// This is the happy case for DetectRedirect.
			Name:          "EnsureNoRedirect",
			Skylink:       "4CCcCO73xMbehYaK7bjDGCtW0GwOL6Swl-lNY52Pb_APzA/",
			ExpectedError: "",
		},
	}

	r = tg.Renters()[0]
	r.Client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return errors.New(redirectErrStr)
	}
	// Run the tests.
	for _, test := range subTests {
		_, _, err := r.SkynetSkylinkGet(test.Skylink)
		if err == nil && test.ExpectedError != "" {
			t.Fatalf("%s failed: %+v\n", test.Name, err)
		}
		if err != nil && (test.ExpectedError == "" || !strings.Contains(err.Error(), test.ExpectedError)) {
			t.Fatalf("%s failed: expected error '%s', got '%+v'\n", test.Name, test.ExpectedError, err)
		}
	}
}