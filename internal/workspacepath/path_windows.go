package workspacepath

import (
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func Canonical(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if n >= uint32(len(buffer)) {
		return "", syscall.ENAMETOOLONG
	}
	resolved := windows.UTF16ToString(buffer[:n])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(resolved, `\\?\`), nil
}

func IsLink(info os.FileInfo) bool {
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}
