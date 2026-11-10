package main

import "testing"

func TestVerifyBuild(t *testing.T) {
	oldVersion, oldCommit := version, commit
	t.Cleanup(func() { version, commit = oldVersion, oldCommit })
	commit = "0123456789abcdef0123456789abcdef01234567"
	for _, tt := range []struct {
		name, embedded, expected, source string
		valid                            bool
	}{
		{"release", "1.2.3", "v1.2.3", commit, true},
		{"candidate", "1.2.3-rc.1", "v1.2.3-rc.1", commit, true},
		{"snapshot rehearsal", "0.0.0-SNAPSHOT-" + commit, "0.0.0-SNAPSHOT-" + commit, commit, true},
		{"snapshot under release tag", "0.0.0-SNAPSHOT-" + commit, "v1.2.3", commit, false},
		{"wrong version", "1.2.2", "v1.2.3", commit, false},
		{"wrong commit", "1.2.3", "v1.2.3", "different", false},
		{"missing version", "1.2.3", "", commit, false},
		{"missing commit", "1.2.3", "v1.2.3", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			version = tt.embedded
			if err := verifyBuild(tt.expected, tt.source); (err == nil) != tt.valid {
				t.Fatalf("verifyBuild() = %v, want valid=%v", err, tt.valid)
			}
		})
	}
}
