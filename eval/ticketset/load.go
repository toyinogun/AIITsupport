package ticketset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// Load reads the ticket file, checks it against the directory truth and the
// kb file's as_of, and returns the tickets with the file hash. Every problem
// is reported in one error and no tickets are returned (AC-9).
func Load(ticketsPath, directoryPath, kbPath string) (Set, error) {
	data, err := os.ReadFile(ticketsPath) //nolint:gosec // G304: the path comes from the caller
	if err != nil {
		return Set{}, fmt.Errorf("read ticket file: %w", err)
	}
	model, err := directory.Load(directoryPath, "")
	if err != nil {
		return Set{}, fmt.Errorf("load directory truth: %w", err)
	}
	kbAsOf, err := readKBAsOf(kbPath)
	if err != nil {
		return Set{}, err
	}
	return parse(data, model, kbAsOf)
}

// readKBAsOf reads only the as_of date of the knowledge base seed.
func readKBAsOf(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path comes from the caller
	if err != nil {
		return "", fmt.Errorf("read kb file: %w", err)
	}
	var kb struct {
		AsOf string `yaml:"as_of"`
	}
	if err := yaml.Unmarshal(data, &kb); err != nil {
		return "", fmt.Errorf("decode kb file: %w", err)
	}
	return kb.AsOf, nil
}

func parse(data []byte, model directory.Model, kbAsOf string) (Set, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return Set{}, errors.New("tickets: file is empty")
		}
		return Set{}, fmt.Errorf("tickets: decode: %w", err)
	}

	c := checker{model: model}
	c.checkFile(f, kbAsOf)
	for i, t := range f.Tickets {
		c.checkTicket(i, t)
	}
	c.checkCounts(f.Tickets)
	if err := errors.Join(c.problems...); err != nil {
		return Set{}, err
	}

	sum := sha256.Sum256(data)
	return Set{Tickets: slices.Clone(f.Tickets), Hash: hex.EncodeToString(sum[:]), AsOf: f.AsOf}, nil
}

// checker collects problems, each line starting with the ticket id.
type checker struct {
	model    directory.Model
	problems []error
}

func (c *checker) add(id, format string, args ...any) {
	c.problems = append(c.problems, fmt.Errorf("%s: %s", id, fmt.Sprintf(format, args...)))
}
