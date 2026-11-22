// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/require"
)

type translationFixtureChange struct {
	Name   string `json:"name"`
	Base   string `json:"base"`
	Old    string `json:"old"`
	New    string `json:"new"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

type translationFixtureCorpus struct {
	Namespace string                              `json:"namespace"`
	Targets   map[string]string                   `json:"targets"`
	Valid     map[string]string                   `json:"valid"`
	Negative  []translationFixtureChange          `json:"negative"`
	Deferred  map[string]translationFixtureChange `json:"deferred"`
}

func loadTranslationFixtures(t *testing.T) translationFixtureCorpus {
	t.Helper()
	data, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus translationFixtureCorpus
	require.NoError(t, json.Unmarshal(data, &corpus))
	return corpus
}

func (c translationFixtureCorpus) changed(t *testing.T, change translationFixtureChange) []byte {
	t.Helper()
	base, ok := c.Valid[change.Base]
	require.True(t, ok, "unknown fixture base %s", change.Base)
	require.Equal(t, 1, strings.Count(base, change.Old), "ambiguous fixture substitution %s", change.Name)
	return []byte(strings.Replace(base, change.Old, change.New, 1))
}

func TestTranslationTargetFixtures(t *testing.T) {
	corpus := loadTranslationFixtures(t)
	for kind, source := range corpus.Targets {
		t.Run(kind, func(t *testing.T) {
			parsed, _, err := validate.NewParser().ParseResource(bytes.NewBufferString(source))
			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.Equal(t, kind, parsed.Kind)
		})
	}
	for _, change := range corpus.Negative {
		t.Run(change.Name, func(t *testing.T) {
			corpus.changed(t, change)
		})
	}
	for name, change := range corpus.Deferred {
		t.Run(name, func(t *testing.T) {
			corpus.changed(t, change)
		})
	}
}
