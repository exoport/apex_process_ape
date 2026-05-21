package output

import (
	"encoding/json"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Format represents an output format.
type Format string

const (
	FormatHuman Format = "human"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
)

// Print writes data to w in the specified format.
// For FormatHuman, data is printed using fmt.Fprintf with %+v.
// For FormatJSON, encoding/json with indentation is used.
// For FormatYAML, gopkg.in/yaml.v3 is used.
func Print(w io.Writer, format Format, data any) error {
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer enc.Close()
		return enc.Encode(data)
	default:
		_, err := fmt.Fprintf(w, "%+v\n", data)
		return err
	}
}
