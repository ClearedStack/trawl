package siteform

import (
	_ "embed"
	"testing"

	"github.com/ClearedStack/trawl/internal/tooltest"
)

//go:embed match.yaml
var matchYAML []byte

func TestFixtures(t *testing.T) {
	tooltest.Run(t, New(), matchYAML, "testdata")
}
