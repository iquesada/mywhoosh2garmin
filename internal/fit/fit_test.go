package fit

import "testing"

// header returns a minimal 14-byte FIT header for tests.
func header() []byte {
	return []byte{14, 0x20, 0, 0, 0, 0, 0, 0, '.', 'F', 'I', 'T', 0, 0}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{"valid 14-byte header", header(), false},
		{"valid 12-byte header", append([]byte{12}, header()[1:12]...), false},
		{"too short", []byte{14, 0, 0}, true},
		{"bad header size", append([]byte{20}, header()[1:]...), true},
		{"missing signature", []byte{14, 0, 0, 0, 0, 0, 0, 0, 'X', 'F', 'I', 'T', 0, 0}, true},
		{"html error page", []byte("<html><body>Access Denied</body></html>"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
