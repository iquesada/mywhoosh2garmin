// Package fit holds minimal helpers for FIT (Flexible and Interoperable Data
// Transfer) files.
package fit

import "fmt"

// minHeaderSize is the size of the smallest valid FIT file header.
const minHeaderSize = 12

// Validate checks that data starts with a FIT file header. It does not parse
// or verify the rest of the file.
func Validate(data []byte) error {
	if len(data) < minHeaderSize {
		return fmt.Errorf("not a FIT file: %d bytes is shorter than a FIT header", len(data))
	}
	headerSize := int(data[0])
	if headerSize != 12 && headerSize != 14 {
		return fmt.Errorf("not a FIT file: unexpected header size %d", headerSize)
	}
	if string(data[8:12]) != ".FIT" {
		return fmt.Errorf("not a FIT file: missing .FIT signature")
	}
	return nil
}
