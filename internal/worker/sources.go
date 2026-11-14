package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ClearedStack/trawl/internal/normalize"
)

// Source is one public source Trawl reads on every sweep: which sub-tool
// reads it, by source and format, and the address to read.
type Source struct {
	Source string `json:"source"`
	Format string `json:"format"`
	Fetch  string `json:"fetch"`
}

// MaxSources bounds the sources file.
const MaxSources = 500

// ParseSources reads a sources file: a JSON object with one key,
// "sources", holding a list of entries with "source", "format" and
// "fetch". Unknown keys are refused, and every address must be one Trawl
// would agree to read: http or https, on a public host.
func ParseSources(data []byte) ([]Source, error) {
	var file struct {
		Sources *[]Source `json:"sources"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, errors.New("the sources file is not the expected JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("the sources file holds more than one value")
	}
	if file.Sources == nil {
		return nil, errors.New("the sources file has no \"sources\" list")
	}
	if len(*file.Sources) > MaxSources {
		return nil, fmt.Errorf("the sources file lists more than %d sources", MaxSources)
	}
	for i, s := range *file.Sources {
		if s.Source == "" || s.Format == "" {
			return nil, fmt.Errorf("source %d needs \"source\" and \"format\"", i+1)
		}
		if _, err := normalize.Normalize(s.Fetch); err != nil {
			return nil, fmt.Errorf("source %d: \"fetch\" is not a public http or https address", i+1)
		}
	}
	return *file.Sources, nil
}
