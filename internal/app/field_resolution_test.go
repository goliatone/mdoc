package app

import (
	"strings"
	"testing"
)

func TestPublishResumeCommandNeverContainsFieldValues(t *testing.T) {
	options := CommonOptions{
		Config: ".mdoc.yaml", Profile: "review",
		FieldFiles: []string{"secret-fields.yaml"}, FieldAssignments: []string{`/token="do-not-print"`},
	}
	command := publishResumeCommand(options)
	for _, forbidden := range []string{"secret-fields.yaml", "do-not-print", "--field", "--fields-file"} {
		if strings.Contains(command, forbidden) {
			t.Fatalf("resume command leaked %q: %s", forbidden, command)
		}
	}
}
