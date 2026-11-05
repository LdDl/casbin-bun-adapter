package casbinbunadapter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJournalIdentifiers(t *testing.T) {
	for _, name := range []string{"", "a\x00b", string([]byte{0xff}), strings.Repeat("a", 64), strings.Repeat("я", 32)} {
		err := validateJournalIdentifier(name)
		require.Error(t, err)
	}
	for _, name := range []string{"journal", "schema.with.dot", `quotes" and '`, "$body$", strings.Repeat("a", 63)} {
		err := validateJournalIdentifier(name)
		require.NoError(t, err)
	}
}
