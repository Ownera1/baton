// Copied from collaborative-work (same author).
package guard

import (
	"bytes"
	"os/exec"
)

func runPS(args ...string) (string, error) {
	cmd := exec.Command("/bin/ps", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}
