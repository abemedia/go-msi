package oleps_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/abemedia/go-msi/internal/oleps"
	"github.com/abemedia/go-msi/internal/oleps/internal/olepstest"
)

type fixtureTest struct {
	Name   string
	Stream oleps.PropertySetStream
	Golden []byte
}

func loadFixtures(t *testing.T) []fixtureTest {
	t.Helper()
	fixtures, err := olepstest.LoadFixtures(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("load fixtures: %v", err)
	}
	cases := make([]fixtureTest, 0, len(fixtures))
	for name, s := range fixtures {
		data, err := os.ReadFile(filepath.Join("testdata", name+".golden"))
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		cases = append(cases, fixtureTest{Name: name, Stream: s, Golden: data})
	}
	slices.SortFunc(cases, func(a, b fixtureTest) int { return strings.Compare(a.Name, b.Name) })
	return cases
}

func TestMarshal(t *testing.T) {
	for _, tc := range loadFixtures(t) {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := oleps.Marshal(tc.Stream)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if !bytes.Equal(got, tc.Golden) {
				t.Fatalf("Marshal != golden\n got: %x\nwant: %x", got, tc.Golden)
			}
		})
	}
}

func TestUnmarshal(t *testing.T) {
	for _, tc := range loadFixtures(t) {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := oleps.Unmarshal(tc.Golden)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if diff := cmp.Diff(tc.Stream, got, cmpOpts); diff != "" {
				t.Fatalf("decoded mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
