package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		wantErr    string
		wantStdout string
	}{
		{name: "version", args: []string{"version"}, wantStdout: "dev"},
		{name: "help", args: []string{"help"}, wantStdout: "Commands:"},
		{name: "unknown command", args: []string{"upload"}, wantErr: `unknown command "upload"`},
		{
			name:    "sync without MyWhoosh credentials",
			args:    []string{"sync", "-config-dir", "CONFIG"},
			env:     map[string]string{envMyWhooshEmail: "", envMyWhooshPassword: ""},
			wantErr: "set MYWHOOSH_EMAIL and MYWHOOSH_PASSWORD",
		},
		{name: "bad flag", args: []string{"sync", "-nope"}, wantErr: "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			args := make([]string, len(tt.args))
			for i, a := range tt.args {
				args[i] = strings.ReplaceAll(a, "CONFIG", t.TempDir())
			}
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), args, &stdout, &stderr)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("run() error = %v, want %q", err, tt.wantErr)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
		})
	}
}
