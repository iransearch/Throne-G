//go:build !debug

package parentcheck

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func CheckParentProcess() {
	parentPath, err := getParentExePath(ParentPID)
	if err != nil {
		log.Fatalf("parent check: cannot read parent executable: %v", err)
	}
	parentPath = resolveFinalPath(parentPath)
	parentBase := filepath.Base(parentPath)

	if runtime.GOOS == "windows" {
		validParent := strings.EqualFold(parentBase, "Throne.exe") ||
			strings.EqualFold(parentBase, "IRSpeedyVPN.exe")
		if !validParent {
			log.Fatalf("parent check failed: unexpected parent %q", parentPath)
		}
		return
	}

	selfPath, err := os.Executable()
	if err != nil {
		log.Fatalf("parent check: cannot read own executable: %v", err)
	}
	selfPath = resolveFinalPath(selfPath)

	selfDir := filepath.Dir(selfPath)
	parentDir := filepath.Dir(parentPath)
	if parentDir != selfDir || parentBase != "Throne" {
		log.Fatalf("parent check failed: unexpected parent %q, selfPath is %q", parentPath, selfPath)
	}
}
