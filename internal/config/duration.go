package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from a YAML string such as "2s"
// or "90s", so durations read naturally in the config file.
type Duration time.Duration

// UnmarshalYAML parses a duration string with time.ParseDuration.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf(`duration must be a string such as "2s": %w`, err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String renders the duration like time.Duration does.
func (d Duration) String() string { return time.Duration(d).String() }
