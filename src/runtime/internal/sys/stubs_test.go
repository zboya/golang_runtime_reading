package sys_test

import (
	"runtime/internal/sys"
	"testing"
)

func TestRegSize(t *testing.T) {
	t.Log(sys.RegSize)
}
