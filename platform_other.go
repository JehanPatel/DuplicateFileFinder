//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// This file lets you build and try the tool on macOS or Linux. The real
// target is Windows (see platform_windows.go).

const trashName = "trash folder"
const canPickFolder = false

var extraSkipNames = map[string]bool{}

func systemSkipPaths() []string {
	return []string{"/proc", "/sys", "/dev", "/run"}
}

func listDrives() []string { return nil }

// moveToTrash moves the file into a folder inside your user config folder.
func moveToTrash(path string) error {
	base, err := os.UserConfigDir()
	if err != nil {
		return errors.New("Could not find a place to move the file to.")
	}
	trash := filepath.Join(base, "dupefinder-trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(trash, fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(path)))
	if err := os.Rename(path, dest); err != nil {
		return fmt.Errorf("Could not move the file to the trash folder: %v", err)
	}
	return nil
}

func openBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	_ = exec.Command(name, url).Start()
}

func pickFolder() (string, error) {
	return "", errors.New("The folder dialog is only available on Windows. Type the folder path instead.")
}
