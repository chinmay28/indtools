package extract

import (
	"os"
	"path/filepath"
)

// tempTxt allocates a temporary .txt path for external converters that
// pick the output format from the extension.
type tempTxt struct{ dir, path string }

func newTempTxt() (*tempTxt, error) {
	dir, err := os.MkdirTemp("", "datamill-")
	if err != nil {
		return nil, err
	}
	return &tempTxt{dir: dir, path: filepath.Join(dir, "out.txt")}, nil
}

func (t *tempTxt) read() ([]byte, error) { return os.ReadFile(t.path) }
func (t *tempTxt) cleanup()              { os.RemoveAll(t.dir) }
