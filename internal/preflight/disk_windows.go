//go:build windows

package preflight

import "golang.org/x/sys/windows"

func diskFree(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, availToCaller uint64
	if err := windows.GetDiskFreeSpaceEx(p, &availToCaller, &total, &free); err != nil {
		return 0, err
	}
	return availToCaller, nil
}
